package audit

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func testPool(t *testing.T) *pgxpool.Pool {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		url = "postgres://metap:metap@localhost:5432/metap"
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Skipf("no Postgres available (%v) — skipping e2e test", err)
	}
	if err := pool.Ping(context.Background()); err != nil {
		t.Skipf("no Postgres available (%v) — skipping e2e test", err)
	}
	t.Cleanup(pool.Close)
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
	if err != nil {
		t.Fatal(err)
	}

	rows, err := store.ListForRecord(ctx, tenant, "crm.customers", record)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	if rows[0].Action != "update" || *rows[0].Reason != "fixing a typo" || *rows[0].VersionAfter != 2 {
		t.Fatalf("got %+v", rows[0])
	}
}

func TestRecordRejectsAMismatchedTenantID(t *testing.T) {
	store := NewPostgresStore(testPool(t))
	entryTenant := uuid.New()
	callerTenant := uuid.New()
	err := store.Record(context.Background(), callerTenant, Entry{
		TenantID: entryTenant, Entity: "crm.customers", RecordID: uuid.New(),
		Action: ActionCreate, Diff: JsonObject{}, OccurredAt: time.Now(),
	})
	if err == nil {
		t.Fatal("expected an error for mismatched tenant ids")
	}
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
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
	}

	rows, err := store.ListForRecord(ctx, tenant, "crm.customers", record)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("got %d rows", len(rows))
	}
	for i := 0; i < len(rows)-1; i++ {
		if rows[i].OccurredAt.Before(rows[i+1].OccurredAt) {
			t.Fatal("expected newest-first ordering")
		}
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
	if err != nil {
		t.Fatal(err)
	}
	rows, _ := store.ListForRecord(ctx, tenant, "crm.customers", record)
	var entry map[string]json.RawMessage
	_ = json.Unmarshal(rows[0].Diff, &entry)
	var nameEntry map[string]json.RawMessage
	_ = json.Unmarshal(entry["name"], &nameEntry)
	if string(nameEntry["before"]) != "null" {
		t.Fatalf("got before=%s, want null", nameEntry["before"])
	}
}

// Partitioned audit table (migration 0038): a row lands in its month partition; a row for a month
// with no partition yet lands in the default partition and is moved into its month when
// EnsurePartitions creates it; nothing is lost either way.
func TestAuditRowsRouteToMonthPartitionsAndDefaultRowsMoveWhenThePartitionIsCreated(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	if present, err := PartitioningPresent(ctx, pool); err != nil || !present {
		t.Skipf("migration 0038 not applied (present=%v err=%v)", present, err)
	}
	tenant := uuid.New()
	t.Cleanup(func() {
		pool.Exec(ctx, `DELETE FROM metadata.audit_trail_entries WHERE tenant_id = $1`, tenant)
	})

	store := NewPostgresStore(pool)
	now := time.Now().UTC()
	if err := store.Record(ctx, tenant, Entry{TenantID: tenant, Entity: "test.e", RecordID: uuid.New(), Action: ActionCreate, OccurredAt: now}); err != nil {
		t.Fatal(err)
	}
	var partition string
	if err := pool.QueryRow(ctx,
		`SELECT c.relname FROM metadata.audit_trail_entries a JOIN pg_class c ON c.oid = a.tableoid WHERE a.tenant_id = $1`, tenant).Scan(&partition); err != nil {
		t.Fatal(err)
	}
	if want := "audit_trail_entries_" + now.Format("200601"); partition != want {
		t.Fatalf("current-month row landed in %s, want %s", partition, want)
	}

	// The month right after the last existing partition has none yet, so it goes to the default
	// partition. (Computed from the catalog, not a fixed offset, so reruns against the same
	// database keep exercising the default-partition path.)
	var lastMonth time.Time
	if err := pool.QueryRow(ctx,
		`SELECT max(to_date(substring(relname from '[0-9]{6}$'), 'YYYYMM'))::timestamptz
		 FROM pg_class WHERE relname ~ '^audit_trail_entries_[0-9]{6}$'`).Scan(&lastMonth); err != nil {
		t.Fatal(err)
	}
	future := time.Date(lastMonth.Year(), lastMonth.Month()+1, 15, 12, 0, 0, 0, time.UTC)
	monthsAhead := (future.Year()-now.Year())*12 + int(future.Month()) - int(now.Month())
	futureRecord := uuid.New()
	if err := store.Record(ctx, tenant, Entry{TenantID: tenant, Entity: "test.e", RecordID: futureRecord, Action: ActionCreate, OccurredAt: future}); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx,
		`SELECT c.relname FROM metadata.audit_trail_entries a JOIN pg_class c ON c.oid = a.tableoid WHERE a.record_id = $1`, futureRecord).Scan(&partition); err != nil {
		t.Fatal(err)
	}
	if partition != "audit_trail_entries_default" {
		t.Fatalf("future row landed in %s, want the default partition", partition)
	}

	futurePart := "audit_trail_entries_" + future.Format("200601")
	t.Cleanup(func() {
		pool.Exec(ctx, `DELETE FROM metadata.audit_trail_entries WHERE record_id = $1`, futureRecord)
	})
	if _, err := EnsurePartitions(ctx, pool, monthsAhead); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx,
		`SELECT c.relname FROM metadata.audit_trail_entries a JOIN pg_class c ON c.oid = a.tableoid WHERE a.record_id = $1`, futureRecord).Scan(&partition); err != nil {
		t.Fatal(err)
	}
	if partition != futurePart {
		t.Fatalf("after EnsurePartitions the row is in %s, want %s", partition, futurePart)
	}
	var total int
	pool.QueryRow(ctx, `SELECT count(*) FROM metadata.audit_trail_entries WHERE tenant_id = $1`, tenant).Scan(&total)
	if total != 2 {
		t.Fatalf("no row may be lost, got %d", total)
	}
	if again, err := EnsurePartitions(ctx, pool, monthsAhead); err != nil || again != 0 {
		t.Fatalf("second run must be a no-op: created=%d err=%v", again, err)
	}
}
