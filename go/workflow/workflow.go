// Package workflow is metadata-driven workflow: initial-status resolution, transition lookup,
// guard/validator evaluation, the workflow_events audit write, and outbox emits. Ported from
// metap's Rust metap-workflow crate. No struct holding state — a plain function module is the
// faithful equivalent, matching the Rust original's own reasoning (its one constructor
// dependency was only ever used to reach a function that ignores "self" anyway).
package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/metap-org/metap-go/infra"
	"github.com/metap-org/metap-go/metadata"
	"github.com/metap-org/metap-go/permission"
)

// DBTX is satisfied by *pgxpool.Pool and pgx.Tx — the Go equivalent of the Rust
// impl sqlx::PgExecutor<'c> genericity every function here uses.
type DBTX interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type JSONObject = map[string]json.RawMessage

// GetInitialStatus: if the entity has no workflow, there's no initial status. If the caller
// already supplied a value for the state field, that wins (an explicit status in data isn't
// overwritten). Otherwise falls back to workflow.InitialState.
func GetInitialStatus(entity metadata.EntityDefinition, data JSONObject) (string, bool) {
	if entity.Workflow == nil {
		return "", false
	}
	if raw, ok := data[entity.Workflow.StateField]; ok {
		var explicit string
		if err := json.Unmarshal(raw, &explicit); err == nil {
			return explicit, true
		}
	}
	return entity.Workflow.InitialState, true
}

func FindTransition(entity metadata.EntityDefinition, action, fromState string) *metadata.WorkflowTransition {
	if entity.Workflow == nil {
		return nil
	}
	for i := range entity.Workflow.Transitions {
		t := &entity.Workflow.Transitions[i]
		if t.Action == action && t.From == fromState {
			return t
		}
	}
	return nil
}

func jsonObjectToRaw(data JSONObject) json.RawMessage {
	b, _ := json.Marshal(data)
	return b
}

// RunGuard: nil error if the guard passes or there is none. The guard itself is a
// PolicyCondition, not a function.
func RunGuard(transition *metadata.WorkflowTransition, data JSONObject, context *permission.RequestContext) error {
	if transition.Guard == nil {
		return nil
	}
	result := permission.EvaluateCondition(*transition.Guard, jsonObjectToRaw(data), context)
	if result.IsPassed() {
		return nil
	}
	reason, ok := result.Reason()
	if !ok {
		reason = "guard failed"
	}
	return fmt.Errorf("%s", reason)
}

// RunValidator is the other half of the "condition vs. validator" split — same PolicyCondition
// type and evaluator as RunGuard, but meant to be called against nextData (the record's data
// *after* the caller's transition payload has been merged in and field-validated, *before*
// ApplySetFields runs), not the pre-transition existing.data RunGuard checks. Lets a transition
// require its payload to actually contain something — something RunGuard alone can never
// express, since it only ever sees data from before the caller's submission.
func RunValidator(transition *metadata.WorkflowTransition, nextData JSONObject, context *permission.RequestContext) error {
	if transition.Validator == nil {
		return nil
	}
	result := permission.EvaluateCondition(*transition.Validator, jsonObjectToRaw(nextData), context)
	if result.IsPassed() {
		return nil
	}
	reason, ok := result.Reason()
	if !ok {
		reason = "validator failed"
	}
	return fmt.Errorf("%s", reason)
}

// ApplySetFields is the declarative post-function: writes transition.SetFields into data in
// place, each value resolved via permission.ResolveValue (a literal, or fromContext — e.g.
// assignee pulled from the caller's own userId). Called after RunValidator passes and before the
// record is written, so these system-computed values are never themselves subject to a
// validator meant for user-submitted payload fields. A no-op when the transition declares no
// SetFields.
func ApplySetFields(transition *metadata.WorkflowTransition, data JSONObject, context *permission.RequestContext) {
	for fieldName, value := range transition.SetFields {
		data[fieldName] = permission.ResolveValue(value, context)
	}
}

func parseContextIDs(context *permission.RequestContext) (uuid.UUID, *uuid.UUID, error) {
	tenantID, err := uuid.Parse(context.TenantID)
	if err != nil {
		return uuid.UUID{}, nil, err
	}
	var actor *uuid.UUID
	if context.UserID != "" {
		a, err := uuid.Parse(context.UserID)
		if err != nil {
			return uuid.UUID{}, nil, err
		}
		actor = &a
	}
	return tenantID, actor, nil
}

// RecordEvent is an append-only audit log — same "never mutate the past" contract as
// workflow_events' role everywhere else in this project.
func RecordEvent(ctx context.Context, db DBTX, entity metadata.EntityDefinition, recordID uuid.UUID, action, fromState, toState string, context_ *permission.RequestContext) error {
	tenantID, actor, err := parseContextIDs(context_)
	if err != nil {
		return err
	}
	_, err = db.Exec(ctx,
		`INSERT INTO workflow_events (tenant_id, entity, record_id, action, from_state, to_state, actor)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		tenantID, entity.Name, recordID, action, fromState, toState, actor)
	return err
}

// WorkflowEvent is one row of workflow_events — the read side of RecordEvent's append-only audit
// log. Generic across every entity/app (a plain data row, no business meaning attached).
// WorkflowEvent mirrors the Rust WorkflowEvent — deliberately snake_case on the wire (its own
// #[derive(Serialize)] emits Rust field names verbatim, inconsistent with the rest of this
// surface's camelCase, but left as-is since a real shipped frontend reads these exact keys; see
// routes_workflow_events.go's own doc comment).
type WorkflowEvent struct {
	ID        uuid.UUID  `json:"id"`
	Entity    string     `json:"entity"`
	RecordID  uuid.UUID  `json:"record_id"`
	Action    string     `json:"action"`
	FromState string     `json:"from_state"`
	ToState   string     `json:"to_state"`
	Actor     *uuid.UUID `json:"actor,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
}

// ListEvents returns the chronological transition history for one record — tenant-scoped like
// every other read here.
func ListEvents(ctx context.Context, db DBTX, tenantID uuid.UUID, entity string, recordID uuid.UUID) ([]WorkflowEvent, error) {
	rows, err := db.Query(ctx,
		`SELECT id, entity, record_id, action, from_state, to_state, actor, created_at
		 FROM workflow_events WHERE tenant_id = $1 AND entity = $2 AND record_id = $3
		 ORDER BY created_at ASC`,
		tenantID, entity, recordID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// make(..., 0), not var events []WorkflowEvent — a nil Go slice marshals to JSON null, not
	// [], diverging from the Rust Vec<WorkflowEvent> wire shape a REST client expects on a
	// record with no transition history yet.
	events := make([]WorkflowEvent, 0)
	for rows.Next() {
		var e WorkflowEvent
		if err := rows.Scan(&e.ID, &e.Entity, &e.RecordID, &e.Action, &e.FromState, &e.ToState, &e.Actor, &e.CreatedAt); err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

func EmitTransitioned(ctx context.Context, db DBTX, entity metadata.EntityDefinition, tenantID, recordID uuid.UUID, action, fromState, toState string, actor *uuid.UUID) error {
	payload, _ := json.Marshal(map[string]any{
		"tenantId": tenantID, "recordId": recordID, "action": action, "from": fromState, "to": toState, "actor": actor,
	})
	event := &infra.OutboxEvent{
		Topic:         entity.Name + ".workflow.transitioned",
		AggregateType: entity.Name,
		AggregateID:   recordID,
		Payload:       payload,
	}
	return infra.EnqueueOutboxEvent(ctx, db, event)
}

// EmitCreated: tenantId in the payload — needed for metap-cron's on_record_event trigger (not
// yet ported) to scope its cron_jobs lookup by tenant; without it a record-event consumer would
// have no tenant-safe way to know which tenant's jobs to match.
func EmitCreated(ctx context.Context, db DBTX, entity metadata.EntityDefinition, tenantID, recordID uuid.UUID, data JSONObject) error {
	payload, _ := json.Marshal(map[string]any{"tenantId": tenantID, "recordId": recordID, "data": data})
	event := &infra.OutboxEvent{
		Topic:         entity.Name + ".record.created",
		AggregateType: entity.Name,
		AggregateID:   recordID,
		Payload:       payload,
	}
	return infra.EnqueueOutboxEvent(ctx, db, event)
}

func EmitDeleted(ctx context.Context, db DBTX, entity metadata.EntityDefinition, tenantID, recordID uuid.UUID) error {
	payload, _ := json.Marshal(map[string]any{"tenantId": tenantID, "recordId": recordID})
	event := &infra.OutboxEvent{
		Topic:         entity.Name + ".record.deleted",
		AggregateType: entity.Name,
		AggregateID:   recordID,
		Payload:       payload,
	}
	return infra.EnqueueOutboxEvent(ctx, db, event)
}

func EmitUpdated(ctx context.Context, db DBTX, entity metadata.EntityDefinition, tenantID, recordID uuid.UUID, data JSONObject, version int32) error {
	payload, _ := json.Marshal(map[string]any{"tenantId": tenantID, "recordId": recordID, "data": data, "version": version})
	event := &infra.OutboxEvent{
		Topic:         entity.Name + ".record.updated",
		AggregateType: entity.Name,
		AggregateID:   recordID,
		Payload:       payload,
	}
	return infra.EnqueueOutboxEvent(ctx, db, event)
}
