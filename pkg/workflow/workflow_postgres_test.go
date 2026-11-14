//go:build integration

package workflow

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/metap-org/metap/pkg/testkit"
	"github.com/stretchr/testify/require"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/metap-org/metap/pkg/metadata"
	"github.com/metap-org/metap/pkg/permission"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := testkit.NewPostgres(t, testkit.WithSearchPath("metadata", "public"))
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

	require.NoError(t, RecordEvent(ctx, pool, entity, recordID, "activate", "draft", "active", rctx))
	t.Cleanup(func() { pool.Exec(context.Background(), "DELETE FROM workflow_events WHERE record_id = $1", recordID) })

	var gotEntity, gotAction, gotFrom, gotTo string
	row := pool.QueryRow(ctx, "SELECT entity, action, from_state, to_state FROM workflow_events WHERE record_id = $1", recordID)
	require.NoError(t, row.Scan(&gotEntity, &gotAction, &gotFrom, &gotTo))
	require.Equal(t, "test.widgets", gotEntity, "got entity=%q action=%q from=%q to=%q", gotEntity, gotAction, gotFrom, gotTo)
	require.Equal(t, "activate", gotAction, "got entity=%q action=%q from=%q to=%q", gotEntity, gotAction, gotFrom, gotTo)
	require.Equal(t, "draft", gotFrom, "got entity=%q action=%q from=%q to=%q", gotEntity, gotAction, gotFrom, gotTo)
	require.Equal(t, "active", gotTo, "got entity=%q action=%q from=%q to=%q", gotEntity, gotAction, gotFrom, gotTo)
}

func TestEmitTransitionedAndEmitCreatedEnqueueOutboxRowsWithCorrectTopics(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	entity := wfTestEntity()
	recordID := uuid.New()
	tenantID := uuid.New()

	require.NoError(t, EmitTransitioned(ctx, pool, entity, tenantID, recordID, "activate", "draft", "active", nil))
	data := JSONObject{"name": mustRaw("Acme")}
	require.NoError(t, EmitCreated(ctx, pool, entity, tenantID, recordID, data))
	t.Cleanup(func() { pool.Exec(context.Background(), "DELETE FROM outbox_events WHERE aggregate_id = $1", recordID) })

	rows, err := pool.Query(ctx, "SELECT topic, payload FROM outbox_events WHERE aggregate_id = $1 ORDER BY created_at", recordID)
	require.NoError(t, err)
	defer rows.Close()

	type row struct {
		topic   string
		payload json.RawMessage
	}
	var got []row
	for rows.Next() {
		var r row
		require.NoError(t, rows.Scan(&r.topic, &r.payload))
		got = append(got, r)
	}
	require.Len(t, got, 2, "expected 2 outbox rows, got %d", len(got))
	require.Equal(t, "test.widgets.workflow.transitioned", got[0].topic, "got topic %q", got[0].topic)
	require.Equal(t, "test.widgets.record.created", got[1].topic, "got topic %q", got[1].topic)

	var transitionedPayload map[string]any
	require.NoError(t, json.Unmarshal(got[0].payload, &transitionedPayload))
	require.Equal(t, tenantID.String(), transitionedPayload["tenantId"], "got tenantId %v", transitionedPayload["tenantId"])

	var createdPayload map[string]any
	require.NoError(t, json.Unmarshal(got[1].payload, &createdPayload))
	require.Equal(t, "Acme", createdPayload["data"].(map[string]any)["name"], "got data.name %v", createdPayload["data"])
	require.Equal(t, tenantID.String(), createdPayload["tenantId"], "got tenantId %v", createdPayload["tenantId"])
}

func TestListEventsReturnsHistoryInChronologicalOrder(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	entity := wfTestEntity()
	recordID := uuid.New()
	tenantID := uuid.New()
	rctx := &permission.RequestContext{TenantID: tenantID.String()}

	require.NoError(t, RecordEvent(ctx, pool, entity, recordID, "create", "", "draft", rctx))
	require.NoError(t, RecordEvent(ctx, pool, entity, recordID, "activate", "draft", "active", rctx))
	t.Cleanup(func() { pool.Exec(context.Background(), "DELETE FROM workflow_events WHERE record_id = $1", recordID) })

	events, err := ListEvents(ctx, pool, tenantID, entity.Name, recordID)
	require.NoError(t, err)
	require.Len(t, events, 2, "expected 2 events, got %d", len(events))
	require.Equal(t, "create", events[0].Action, "expected chronological order, got %q then %q", events[0].Action, events[1].Action)
	require.Equal(t, "activate", events[1].Action, "expected chronological order, got %q then %q", events[0].Action, events[1].Action)
}
