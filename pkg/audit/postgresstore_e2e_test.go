//go:build integration

package audit

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/metap-org/metap/pkg/testkit"
	"github.com/stretchr/testify/require"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := testkit.NewPostgres(t)
	return pool
}

func TestRecordThenListForRecordRoundTrips(t *testing.T) {
	store := NewPostgresStore(testPool(t))
	ctx := context.Background()
	tenant := uuid.New()
	record := uuid.New()
	actor := uuid.New()
	reason := "fixing a typo"
	version := int32(2)

	diff := JsonObject{"name": json.RawMessage(`{"before":"old","after":"new"}`)}
	err := store.Record(ctx, tenant, Entry{
		TenantID: tenant, Entity: "crm.customers", RecordID: record,
		Action: ActionUpdate, ActorUserID: &actor, Reason: &reason,
		Diff: diff, VersionAfter: &version, OccurredAt: time.Now(),
	})
	require.NoError(t, err)

	rows, err := store.Query(ctx, tenant, Query{Entity: "crm.customers", RecordID: record})
	require.NoError(t, err)
	require.Len(t, rows, 1, "got %d rows, want 1", len(rows))
	require.Equal(t, "update", rows[0].Action, "got %+v", rows[0])
	require.Equal(t, "fixing a typo", *rows[0].Reason, "got %+v", rows[0])
	require.EqualValues(t, 2, *rows[0].VersionAfter, "got %+v", rows[0])
}

func TestRecordRejectsAMismatchedTenantID(t *testing.T) {
	store := NewPostgresStore(testPool(t))
	entryTenant := uuid.New()
	callerTenant := uuid.New()
	err := store.Record(context.Background(), callerTenant, Entry{
		TenantID: entryTenant, Entity: "crm.customers", RecordID: uuid.New(),
		Action: ActionCreate, Diff: JsonObject{}, OccurredAt: time.Now(),
	})
	require.Error(t, err, "expected an error for mismatched tenant ids")
}

func TestListForRecordReturnsNewestFirst(t *testing.T) {
	store := NewPostgresStore(testPool(t))
	ctx := context.Background()
	tenant, record := uuid.New(), uuid.New()

	for i := 0; i < 3; i++ {
		err := store.Record(ctx, tenant, Entry{
			TenantID: tenant, Entity: "crm.customers", RecordID: record,
			Action: ActionUpdate, Diff: JsonObject{}, OccurredAt: time.Now(),
		})
		require.NoError(t, err)
		time.Sleep(5 * time.Millisecond)
	}

	rows, err := store.Query(ctx, tenant, Query{Entity: "crm.customers", RecordID: record})
	require.NoError(t, err)
	require.Len(t, rows, 3, "got %d rows", len(rows))
	for i := 0; i < len(rows)-1; i++ {
		require.False(t, rows[i].OccurredAt.Before(rows[i+1].OccurredAt), "expected newest-first ordering")
	}
}

func TestCreateEntryHasNoBeforeStateByConvention(t *testing.T) {
	store := NewPostgresStore(testPool(t))
	ctx := context.Background()
	tenant, record := uuid.New(), uuid.New()

	diff := DiffJSONObjects(JsonObject{}, JsonObject{"name": json.RawMessage(`"Alice"`)})
	err := store.Record(ctx, tenant, Entry{
		TenantID: tenant, Entity: "crm.customers", RecordID: record,
		Action: ActionCreate, Diff: diff, OccurredAt: time.Now(),
	})
	require.NoError(t, err)
	rows, _ := store.Query(ctx, tenant, Query{Entity: "crm.customers", RecordID: record})
	var entry map[string]json.RawMessage
	_ = json.Unmarshal(rows[0].Diff, &entry)
	var nameEntry map[string]json.RawMessage
	_ = json.Unmarshal(entry["name"], &nameEntry)
	require.Equal(t, "null", string(nameEntry["before"]), "got before=%s, want null", nameEntry["before"])
}

// Partitioned audit table (migration 0038): a row lands in its month partition; a row for a month
// with no partition yet lands in the default partition and is moved into its month when
// EnsurePartitions creates it; nothing is lost either way.
func TestAuditRowsRouteToMonthPartitionsAndDefaultRowsMoveWhenThePartitionIsCreated(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	// testkit's database always has every migration applied, 0038 included.
	present, err := PartitioningPresent(ctx, pool)
	require.NoError(t, err, "migration 0038's partitioning missing (present=%v err=%v)", present, err)
	require.True(t, present, "migration 0038's partitioning missing (present=%v err=%v)", present, err)
	tenant := uuid.New()
	t.Cleanup(func() {
		pool.Exec(ctx, `DELETE FROM metadata.audit_trail_entries WHERE tenant_id = $1`, tenant)
	})

	store := NewPostgresStore(pool)
	now := time.Now().UTC()
	require.NoError(t, store.Record(ctx, tenant, Entry{TenantID: tenant, Entity: "test.e", RecordID: uuid.New(), Action: ActionCreate, OccurredAt: now}))
	var partition string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT c.relname FROM metadata.audit_trail_entries a JOIN pg_class c ON c.oid = a.tableoid WHERE a.tenant_id = $1`, tenant).Scan(&partition))
	want := "audit_trail_entries_" + now.Format("200601")
	require.Equal(t, want, partition, "current-month row landed in %s, want %s", partition, want)

	// The month right after the last existing partition has none yet, so it goes to the default
	// partition. (Computed from the catalog, not a fixed offset, so reruns against the same
	// database keep exercising the default-partition path.)
	var lastMonth time.Time
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT max(to_date(substring(relname from '[0-9]{6}$'), 'YYYYMM'))::timestamptz
		 FROM pg_class WHERE relname ~ '^audit_trail_entries_[0-9]{6}$'`).Scan(&lastMonth))
	future := time.Date(lastMonth.Year(), lastMonth.Month()+1, 15, 12, 0, 0, 0, time.UTC)
	monthsAhead := (future.Year()-now.Year())*12 + int(future.Month()) - int(now.Month())
	futureRecord := uuid.New()
	require.NoError(t, store.Record(ctx, tenant, Entry{TenantID: tenant, Entity: "test.e", RecordID: futureRecord, Action: ActionCreate, OccurredAt: future}))
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT c.relname FROM metadata.audit_trail_entries a JOIN pg_class c ON c.oid = a.tableoid WHERE a.record_id = $1`, futureRecord).Scan(&partition))
	require.Equal(t, "audit_trail_entries_default", partition, "future row landed in %s, want the default partition", partition)

	futurePart := "audit_trail_entries_" + future.Format("200601")
	t.Cleanup(func() {
		pool.Exec(ctx, `DELETE FROM metadata.audit_trail_entries WHERE record_id = $1`, futureRecord)
	})
	_, err = EnsurePartitions(ctx, pool, monthsAhead)
	require.NoError(t, err)
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT c.relname FROM metadata.audit_trail_entries a JOIN pg_class c ON c.oid = a.tableoid WHERE a.record_id = $1`, futureRecord).Scan(&partition))
	require.Equal(t, futurePart, partition, "after EnsurePartitions the row is in %s, want %s", partition, futurePart)
	var total int
	pool.QueryRow(ctx, `SELECT count(*) FROM metadata.audit_trail_entries WHERE tenant_id = $1`, tenant).Scan(&total)
	require.Equal(t, 2, total, "no row may be lost, got %d", total)
	again, err := EnsurePartitions(ctx, pool, monthsAhead)
	require.NoError(t, err, "second run must be a no-op: created=%d err=", again)
	require.Equal(t, 0, again, "second run must be a no-op: created=%d err=%v", again, err)
}

// A parent's history includes its children's entries (only for the child entities asked for),
// pages by (occurredAt, id) without gaps or repeats, and filters by action and entity.
func TestQueryIncludesChildrenPagesAndFilters(t *testing.T) {
	store := NewPostgresStore(testPool(t))
	ctx := context.Background()
	tenant := uuid.New()
	zone := uuid.New()
	zoneEntity := "waf.zones"
	base := time.Now().UTC().Add(-time.Hour)

	record := func(entity string, id uuid.UUID, action AuditAction, parent bool, at time.Time) {
		e := Entry{TenantID: tenant, Entity: entity, RecordID: id, Action: action, Diff: JsonObject{}, OccurredAt: at}
		if parent {
			e.ParentEntity, e.ParentID = &zoneEntity, &zone
		}
		require.NoError(t, store.Record(ctx, tenant, e))
	}
	record(zoneEntity, zone, ActionCreate, false, base)
	rule := uuid.New()
	for i := 0; i < 5; i++ {
		action := ActionUpdate
		if i == 0 {
			action = ActionCreate
		}
		record("waf.firewall_rules", rule, action, true, base.Add(time.Duration(i+1)*time.Minute))
	}
	record("waf.ddos_policies", uuid.New(), ActionCreate, true, base.Add(10*time.Minute))
	// Another zone's child must never show up.
	other := uuid.New()
	e := Entry{TenantID: tenant, Entity: "waf.firewall_rules", RecordID: uuid.New(), Action: ActionCreate,
		Diff: JsonObject{}, OccurredAt: base, ParentEntity: &zoneEntity, ParentID: &other}
	require.NoError(t, store.Record(ctx, tenant, e))

	own, err := store.Query(ctx, tenant, Query{Entity: zoneEntity, RecordID: zone, Limit: 50})
	require.NoError(t, err)
	require.Len(t, own, 1, "without ChildEntities only the zone's own entry")

	children := []string{"waf.firewall_rules", "waf.ddos_policies"}
	all, err := store.Query(ctx, tenant, Query{Entity: zoneEntity, RecordID: zone, ChildEntities: children, Limit: 50})
	require.NoError(t, err)
	require.Len(t, all, 7)
	require.Equal(t, "waf.ddos_policies", all[0].Entity, "newest first")
	require.Equal(t, zoneEntity, *all[1].ParentEntity)

	onlyRules, err := store.Query(ctx, tenant, Query{Entity: zoneEntity, RecordID: zone, ChildEntities: children[:1], Limit: 50})
	require.NoError(t, err)
	require.Len(t, onlyRules, 6, "a child entity not listed is left out")

	// Pages of 3: the store returns Limit+1 rows; continuing after the 3rd yields the rest.
	page1, err := store.Query(ctx, tenant, Query{Entity: zoneEntity, RecordID: zone, ChildEntities: children, Limit: 3})
	require.NoError(t, err)
	require.Len(t, page1, 4)
	last := page1[2]
	page2, err := store.Query(ctx, tenant, Query{Entity: zoneEntity, RecordID: zone, ChildEntities: children, Limit: 3,
		After: &Cursor{OccurredAt: last.OccurredAt, ID: last.ID}})
	require.NoError(t, err)
	require.Len(t, page2, 4)
	require.Equal(t, all[3].ID, page2[0].ID, "page 2 starts right after page 1")

	creates, err := store.Query(ctx, tenant, Query{Entity: zoneEntity, RecordID: zone, ChildEntities: children,
		Actions: []string{"create"}, Limit: 50})
	require.NoError(t, err)
	require.Len(t, creates, 3)

	ddosOnly, err := store.Query(ctx, tenant, Query{Entity: zoneEntity, RecordID: zone, ChildEntities: children,
		Entities: []string{"waf.ddos_policies"}, Limit: 50})
	require.NoError(t, err)
	require.Len(t, ddosOnly, 1)
}

// BackfillParents points pre-existing entries at their record's current parent, leaves records
// with no parent (or no row) alone, and is a no-op on a second run.
func TestBackfillParentsFillsFromTheRecordsParentColumn(t *testing.T) {
	pool := testPool(t)
	store := NewPostgresStore(pool)
	ctx := context.Background()
	tenant := uuid.New()
	_, err := pool.Exec(ctx, `CREATE TABLE public.test_audit_rules (id uuid PRIMARY KEY, tenant_id uuid NOT NULL, "zoneId" uuid)`)
	require.NoError(t, err)
	t.Cleanup(func() { pool.Exec(context.Background(), `DROP TABLE IF EXISTS public.test_audit_rules`) })

	zone, withZone, noZone, gone := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	_, err = pool.Exec(ctx, `INSERT INTO public.test_audit_rules VALUES ($1, $3, $4), ($2, $3, NULL)`, withZone, noZone, tenant, zone)
	require.NoError(t, err)
	for _, id := range []uuid.UUID{withZone, withZone, noZone, gone} {
		require.NoError(t, store.Record(ctx, tenant, Entry{TenantID: tenant, Entity: "t.rules", RecordID: id,
			Action: ActionUpdate, Diff: JsonObject{}, OccurredAt: time.Now()}))
	}

	n, err := BackfillParents(ctx, pool, "t.rules", "public.test_audit_rules", "zoneId", "t.zones")
	require.NoError(t, err)
	require.EqualValues(t, 2, n)
	rows, err := store.Query(ctx, tenant, Query{Entity: "t.zones", RecordID: zone, ChildEntities: []string{"t.rules"}, Limit: 50})
	require.NoError(t, err)
	require.Len(t, rows, 2)

	again, err := BackfillParents(ctx, pool, "t.rules", "public.test_audit_rules", "zoneId", "t.zones")
	require.NoError(t, err)
	require.EqualValues(t, 0, again)
}
