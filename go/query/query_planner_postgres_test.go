package query

// E2E tests against a real Postgres, proving the SQL PlanList/PlanAggregate generate actually
// executes and behaves correctly — not just that it compiles to the expected string. A
// deliberately reduced subset of the Rust original's tests/query_planner_postgres.rs (697
// lines) and src/aggregate/tests.rs's own e2e cases, given this crate's overall size for this
// porting pass — focused on the two highest-risk behaviors those files exist to prove: keyset
// pagination across a NULL-containing sort column (the AUDIT_2.md bug class this whole cursor
// design exists to avoid), and that PlanAggregate's generated GROUP BY/aggregate SQL actually
// groups and counts real rows correctly. query_planner_test.go/aggregate_test.go already cover
// the pure planning-logic branches without a database.

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/metap-org/metap-go/metadata"
	"github.com/metap-org/metap-go/permission"
)

func qpTestPool(t *testing.T) *pgxpool.Pool {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		url = "postgres://metap:metap@localhost:5432/metap"
	}
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, "SET search_path TO metadata, public")
		return err
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Skipf("no Postgres available (%v) — skipping e2e test", err)
	}
	if err := pool.Ping(context.Background()); err != nil {
		t.Skipf("no Postgres available (%v) — skipping e2e test", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// qpE2ETable creates a dedicated-table fixture (id/tenant_id/deleted/created_at/updated_at +
// data jsonb, same shape every table-per-entity table has) and returns its name plus a cleanup.
func qpE2ETable(t *testing.T, pool *pgxpool.Pool) string {
	table := "public.go_query_test_" + uuid.NewString()[:8]
	_, err := pool.Exec(context.Background(), `CREATE TABLE `+table+` (
		id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
		tenant_id uuid NOT NULL,
		deleted boolean NOT NULL DEFAULT false,
		created_at timestamptz NOT NULL DEFAULT now(),
		updated_at timestamptz NOT NULL DEFAULT now(),
		data jsonb NOT NULL DEFAULT '{}'::jsonb
	)`)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Exec(context.Background(), "DROP TABLE IF EXISTS "+table) })
	return table
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
	var data []byte
	if priority != nil {
		data, _ = json.Marshal(map[string]any{"priority": *priority})
	} else {
		data = []byte(`{}`)
	}
	var id uuid.UUID
	err := pool.QueryRow(context.Background(),
		"INSERT INTO "+table+" (tenant_id, data) VALUES ($1, $2::jsonb) RETURNING id", tenantID, data).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func qpRunList(t *testing.T, pool *pgxpool.Pool, q *PlannedListQuery, table string) []uuid.UUID {
	sql := "SELECT id FROM " + table + " WHERE " + q.WhereSQL + " ORDER BY " + q.OrderBySQL + " LIMIT " + strconv.FormatInt(q.Limit, 10)
	rows, err := pool.Query(context.Background(), sql, ArgsOf(q.Params)...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
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
		if err != nil {
			t.Fatal(err)
		}
		ids := qpRunList(t, pool, q, table)
		if len(ids) == 0 {
			break
		}
		for _, id := range ids {
			if seen[id] {
				t.Fatalf("row %s returned twice across pages — keyset pagination duplicated a row", id)
			}
			seen[id] = true
		}
		// Build the next cursor from the last row of this page, same shape the real HTTP layer
		// would (not yet ported) — read back its own sort value/id directly.
		lastID := ids[len(ids)-1]
		var lastPriority *string
		row := pool.QueryRow(context.Background(), "SELECT data->>'priority' FROM "+table+" WHERE id = $1", lastID)
		if err := row.Scan(&lastPriority); err != nil {
			t.Fatal(err)
		}
		next := EncodeCursor(Cursor{Field: "priority", Value: lastPriority, ID: lastID.String(), Dir: SortDesc})
		cursor = &next
		if len(ids) < 2 {
			break
		}
	}

	if len(seen) != len(expected) {
		t.Fatalf("expected %d rows across all pages, got %d — some rows were dropped", len(expected), len(seen))
	}
	for id := range expected {
		if !seen[id] {
			t.Fatalf("row %s was never returned on any page", id)
		}
	}
}

func TestPlanListTenantScopingActuallyExcludesOtherTenantsRows(t *testing.T) {
	pool := qpTestPool(t)
	table := qpE2ETable(t, pool)
	entity := qpE2EEntity(table)
	reg := qpTestRegistry(t, entity)
	perms := permission.NewPermissionService(nil)

	tenantA, tenantB := uuid.New(), uuid.New()
	qpInsertRow(t, pool, table, tenantA, nil)
	qpInsertRow(t, pool, table, tenantB, nil)

	q, err := PlanList(reg, perms, entity.Name, ListInput{Limit: 50}, qpTestContext(tenantA), nil)
	if err != nil {
		t.Fatal(err)
	}
	ids := qpRunList(t, pool, q, table)
	if len(ids) != 1 {
		t.Fatalf("expected exactly 1 row for tenant A, got %d", len(ids))
	}
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
	reg := qpTestRegistry(t, entity)
	perms := permission.NewPermissionService(nil)
	tenantID := uuid.New()

	for _, country := range []string{"US", "US", "US", "VN", "VN"} {
		data, _ := json.Marshal(map[string]any{"country": country})
		pool.Exec(context.Background(), "INSERT INTO "+table+" (tenant_id, data) VALUES ($1, $2::jsonb)", tenantID, data)
	}

	groupBy := "country"
	q, err := PlanAggregate(reg, perms, entity.Name,
		AggregateInput{Metrics: []AggregateMetric{{Func: AggCount}}, GroupBy: &groupBy, Limit: DefaultGroups},
		qpTestContext(tenantID), nil)
	if err != nil {
		t.Fatal(err)
	}

	rows, err := pool.Query(context.Background(), q.SQL, ArgsOf(q.Params)...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	counts := map[string]float64{}
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var obj map[string]any
		if err := json.Unmarshal(raw, &obj); err != nil {
			t.Fatal(err)
		}
		counts[obj["group"].(string)] = obj["count"].(float64)
	}

	if counts["US"] != 3 || counts["VN"] != 2 {
		t.Fatalf("expected US=3 VN=2, got %+v", counts)
	}
}
