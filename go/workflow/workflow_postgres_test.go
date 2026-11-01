package workflow

// Ported from the Rust metap-workflow crate's tests/workflow_engine_postgres.rs.

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/metap-org/metap-go/metadata"
	"github.com/metap-org/metap-go/permission"
)

func testPool(t *testing.T) *pgxpool.Pool {
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

// wfTestEntity: never actually queried by RecordEvent/EmitTransitioned/EmitCreated — they only
// ever read entity.Name, never entity.TableName (this file writes to workflow_events/
// outbox_events only) — schema-qualified purely so a reader doesn't mistake this for a still-
// valid bare name.
func wfTestEntity() metadata.EntityDefinition {
	return metadata.EntityDefinition{
		Name:      "test.widgets",
		Label:     "Widget",
		TableName: "entities.test_widgets",
	}
}

func TestRecordEventWritesAnAppendOnlyAuditRow(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	entity := wfTestEntity()
	recordID := uuid.New()
	rctx := &permission.RequestContext{TenantID: uuid.NewString(), UserID: uuid.NewString()}

	if err := RecordEvent(ctx, pool, entity, recordID, "activate", "draft", "active", rctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Exec(context.Background(), "DELETE FROM workflow_events WHERE record_id = $1", recordID) })

	var gotEntity, gotAction, gotFrom, gotTo string
	row := pool.QueryRow(ctx, "SELECT entity, action, from_state, to_state FROM workflow_events WHERE record_id = $1", recordID)
	if err := row.Scan(&gotEntity, &gotAction, &gotFrom, &gotTo); err != nil {
		t.Fatal(err)
	}
	if gotEntity != "test.widgets" || gotAction != "activate" || gotFrom != "draft" || gotTo != "active" {
		t.Fatalf("got entity=%q action=%q from=%q to=%q", gotEntity, gotAction, gotFrom, gotTo)
	}
}

func TestEmitTransitionedAndEmitCreatedEnqueueOutboxRowsWithCorrectTopics(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	entity := wfTestEntity()
	recordID := uuid.New()
	tenantID := uuid.New()

	if err := EmitTransitioned(ctx, pool, entity, tenantID, recordID, "activate", "draft", "active", nil); err != nil {
		t.Fatal(err)
	}
	data := JSONObject{"name": mustRaw("Acme")}
	if err := EmitCreated(ctx, pool, entity, tenantID, recordID, data); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Exec(context.Background(), "DELETE FROM outbox_events WHERE aggregate_id = $1", recordID) })

	rows, err := pool.Query(ctx, "SELECT topic, payload FROM outbox_events WHERE aggregate_id = $1 ORDER BY created_at", recordID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	type row struct {
		topic   string
		payload json.RawMessage
	}
	var got []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.topic, &r.payload); err != nil {
			t.Fatal(err)
		}
		got = append(got, r)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 outbox rows, got %d", len(got))
	}
	if got[0].topic != "test.widgets.workflow.transitioned" {
		t.Fatalf("got topic %q", got[0].topic)
	}
	if got[1].topic != "test.widgets.record.created" {
		t.Fatalf("got topic %q", got[1].topic)
	}

	var transitionedPayload map[string]any
	if err := json.Unmarshal(got[0].payload, &transitionedPayload); err != nil {
		t.Fatal(err)
	}
	if transitionedPayload["tenantId"] != tenantID.String() {
		t.Fatalf("got tenantId %v", transitionedPayload["tenantId"])
	}

	var createdPayload map[string]any
	if err := json.Unmarshal(got[1].payload, &createdPayload); err != nil {
		t.Fatal(err)
	}
	if createdPayload["data"].(map[string]any)["name"] != "Acme" {
		t.Fatalf("got data.name %v", createdPayload["data"])
	}
	if createdPayload["tenantId"] != tenantID.String() {
		t.Fatalf("got tenantId %v", createdPayload["tenantId"])
	}
}

// ListEvents has no dedicated test in the Rust crate's own e2e file (confirmed by reading it) —
// new coverage added during the port.
func TestListEventsReturnsHistoryInChronologicalOrder(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	entity := wfTestEntity()
	recordID := uuid.New()
	tenantID := uuid.New()
	rctx := &permission.RequestContext{TenantID: tenantID.String()}

	if err := RecordEvent(ctx, pool, entity, recordID, "create", "", "draft", rctx); err != nil {
		t.Fatal(err)
	}
	if err := RecordEvent(ctx, pool, entity, recordID, "activate", "draft", "active", rctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Exec(context.Background(), "DELETE FROM workflow_events WHERE record_id = $1", recordID) })

	events, err := ListEvents(ctx, pool, tenantID, entity.Name, recordID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if events[0].Action != "create" || events[1].Action != "activate" {
		t.Fatalf("expected chronological order, got %q then %q", events[0].Action, events[1].Action)
	}
}
