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
