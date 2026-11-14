//go:build integration

package query

// E2E tests against a real Postgres, proving the SQL PlanList/PlanAggregate generate actually
// executes and behaves correctly — not just that it compiles to the expected string.

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"

	"github.com/metap-org/metap/pkg/testkit"
	"github.com/stretchr/testify/require"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/metap-org/metap/pkg/metadata"
	"github.com/metap-org/metap/pkg/permission"
	"github.com/metap-org/metap/pkg/reconciler"
)

func qpTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := testkit.NewPostgres(t, testkit.WithSearchPath("metadata", "public"))
	return pool
}

// qpE2ETable reserves a table name for a fixture entity (and drops the table afterwards); the table
// itself is created by qpReconcile, the way production creates one.
func qpE2ETable(t *testing.T, pool *pgxpool.Pool) string {
	table := "public.go_query_test_" + uuid.NewString()[:8]
	t.Cleanup(func() { pool.Exec(context.Background(), "DROP TABLE IF EXISTS "+table) })
	return table
}

func qpReconcile(t *testing.T, pool *pgxpool.Pool, entity metadata.EntityDefinition) {
	t.Helper()
	_, err := reconciler.ReconcileWithScope(context.Background(), pool, uuid.Nil, entity, nil, reconciler.ScopeAllTenants)
	require.NoError(t, err)
}

func qpE2EEntity(table string) metadata.EntityDefinition {
	trueVal := true
	return metadata.EntityDefinition{
		Name:      "test.go_query_e2e",
		Label:     "Widget",
		TableName: table,
		Fields: []metadata.EntityField{
			{Name: "priority", Label: "Priority", Kind: metadata.KindNumber, Sortable: &trueVal},
		},
		ListViews: []metadata.EntityListView{
			{Name: "default", Label: "Default", MaxLimit: 50},
		},
	}
}

func qpInsertRow(t *testing.T, pool *pgxpool.Pool, table string, tenantID uuid.UUID, priority *int) uuid.UUID {
	var id uuid.UUID
	err := pool.QueryRow(context.Background(),
		"INSERT INTO "+table+" (tenant_id, priority) VALUES ($1, $2) RETURNING id", tenantID, priority).Scan(&id)
	require.NoError(t, err)
	return id
}

func qpRunList(t *testing.T, pool *pgxpool.Pool, q *PlannedListQuery, table string) []uuid.UUID {
	sql := "SELECT id FROM " + table + " WHERE " + q.WhereSQL + " ORDER BY " + q.OrderBySQL + " LIMIT " + strconv.FormatInt(q.Limit, 10)
	rows, err := pool.Query(context.Background(), sql, ArgsOf(q.Params)...)
	require.NoError(t, err)
	defer rows.Close()
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		require.NoError(t, rows.Scan(&id))
		ids = append(ids, id)
	}
	return ids
}

// TestKeysetPaginationAcrossANullContainingSortColumnNeverDropsOrDuplicatesRows is the real
// regression test for the NULL-ordering bug this cursor design exists to avoid: a plain
// sort_expr > cursor_value predicate would silently drop every row once a NULL sort value is
// involved, since SQL's three-valued logic makes NULL > x "unknown", never true.
func TestKeysetPaginationAcrossANullContainingSortColumnNeverDropsOrDuplicatesRows(t *testing.T) {
	pool := qpTestPool(t)
	table := qpE2ETable(t, pool)
	entity := qpE2EEntity(table)
	qpReconcile(t, pool, entity)
	reg := qpTestRegistry(t, entity)
	perms := permission.NewPermissionService(nil)
	tenantID := uuid.New()

	// A mix of real priorities and nulls, descending order (nulls sort first under DESC).
	p1, p2, p3 := 1, 2, 3
	expected := map[uuid.UUID]bool{}
	for _, p := range []*int{nil, &p3, nil, &p1, &p2} {
		id := qpInsertRow(t, pool, table, tenantID, p)
		expected[id] = true
	}

	ctx := qpTestContext(tenantID)
	seen := map[uuid.UUID]bool{}
	var cursor *string
	for {
		input := ListInput{Limit: 2, Sort: strPtr("-priority"), Cursor: cursor}
		q, err := PlanList(reg, perms, entity.Name, input, ctx, nil)
		require.NoError(t, err)
		ids := qpRunList(t, pool, q, table)
		if len(ids) == 0 {
			break
		}
		for _, id := range ids {
			require.False(t, seen[id], "row %s returned twice across pages — keyset pagination duplicated a row", id)
			seen[id] = true
		}
		// Build the next cursor from the last row of this page, same shape the real HTTP layer would —
		// read back its own sort value/id directly.
		lastID := ids[len(ids)-1]
		var lastPriority *string
		row := pool.QueryRow(context.Background(), "SELECT priority::text FROM "+table+" WHERE id = $1", lastID)
		require.NoError(t, row.Scan(&lastPriority))
		next := EncodeCursor(Cursor{Field: "priority", Value: lastPriority, ID: lastID.String(), Dir: SortDesc})
		cursor = &next
		if len(ids) < 2 {
			break
		}
	}

	require.Len(t, seen, len(expected), "expected %d rows across all pages, got %d — some rows were dropped", len(expected), len(seen))
	for id := range expected {
		require.True(t, seen[id], "row %s was never returned on any page", id)
	}
}

func TestPlanListTenantScopingActuallyExcludesOtherTenantsRows(t *testing.T) {
	pool := qpTestPool(t)
	table := qpE2ETable(t, pool)
	entity := qpE2EEntity(table)
	qpReconcile(t, pool, entity)
	reg := qpTestRegistry(t, entity)
	perms := permission.NewPermissionService(nil)

	tenantA, tenantB := uuid.New(), uuid.New()
	qpInsertRow(t, pool, table, tenantA, nil)
	qpInsertRow(t, pool, table, tenantB, nil)

	q, err := PlanList(reg, perms, entity.Name, ListInput{Limit: 50}, qpTestContext(tenantA), nil)
	require.NoError(t, err)
	ids := qpRunList(t, pool, q, table)
	require.Len(t, ids, 1, "expected exactly 1 row for tenant A, got %d", len(ids))
}

func TestPlanAggregateGroupByActuallyCountsEachGroupCorrectly(t *testing.T) {
	pool := qpTestPool(t)
	table := qpE2ETable(t, pool)
	trueVal := true
	entity := metadata.EntityDefinition{
		Name:      "test.go_query_agg_e2e",
		Label:     "Widget",
		TableName: table,
		Fields: []metadata.EntityField{
			{Name: "country", Label: "Country", Kind: metadata.KindString, Indexed: &trueVal},
		},
	}
	qpReconcile(t, pool, entity)
	reg := qpTestRegistry(t, entity)
	perms := permission.NewPermissionService(nil)
	tenantID := uuid.New()

	for _, country := range []string{"US", "US", "US", "VN", "VN"} {
		_, err := pool.Exec(context.Background(), "INSERT INTO "+table+" (tenant_id, country) VALUES ($1, $2)", tenantID, country)
		require.NoError(t, err)
	}

	groupBy := "country"
	q, err := PlanAggregate(reg, perms, entity.Name,
		AggregateInput{Metrics: []AggregateMetric{{Func: AggCount}}, GroupBy: &groupBy, Limit: DefaultGroups},
		qpTestContext(tenantID), nil)
	require.NoError(t, err)

	rows, err := pool.Query(context.Background(), q.SQL, ArgsOf(q.Params)...)
	require.NoError(t, err)
	defer rows.Close()

	counts := map[string]float64{}
	for rows.Next() {
		var raw []byte
		require.NoError(t, rows.Scan(&raw))
		var obj map[string]any
		require.NoError(t, json.Unmarshal(raw, &obj))
		counts[obj["group"].(string)] = obj["count"].(float64)
	}

	require.EqualValues(t, 3, counts["US"], "expected US=3 VN=2, got %+v", counts)
	require.EqualValues(t, 2, counts["VN"], "expected US=3 VN=2, got %+v", counts)
}
