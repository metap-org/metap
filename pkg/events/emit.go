package events

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/metap-org/metap/pkg/infra"
)

// Tx is what an emit needs: the business transaction (or any executor that shares its
// search_path), so the event row commits or rolls back with the write that caused it.
type Tx interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// Event is a custom domain event a service emits through the outbox. Payload is any
// JSON-marshalable value — prefer a named struct with json tags so consumers can Decode it.
type Event struct {
	// Topic like "billing.invoice.paid": namespaced, dot-separated, no wildcards, and not a
	// platform record/workflow topic (see ValidateTopic).
	Topic         string
	AggregateType string
	AggregateID   uuid.UUID
	Payload       any
}

// Emit validates and enqueues a custom event in tx. It is the supported way for a service's own
// handler to publish: never publish to RabbitMQ directly from a service (a crash after the
// business commit would lose the event), and never build an infra.OutboxEvent by hand.
func Emit(ctx context.Context, tx Tx, event Event) error {
	if err := ValidateTopic(event.Topic); err != nil {
		return err
	}
	if event.AggregateType == "" || event.AggregateID == uuid.Nil {
		return fmt.Errorf("event %q needs an aggregate type and id", event.Topic)
	}
	return enqueue(ctx, tx, event.Topic, event.AggregateType, event.AggregateID, event.Payload)
}

// enqueue is shared with the platform emitters, which use reserved topics Emit refuses.
func enqueue(ctx context.Context, tx Tx, topic, aggregateType string, aggregateID uuid.UUID, payload any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshaling %s payload: %w", topic, err)
	}
	return infra.EnqueueOutboxEvent(ctx, tx, &infra.OutboxEvent{
		Topic: topic, AggregateType: aggregateType, AggregateID: aggregateID, Payload: raw,
	})
}

// EmitTransitioned enqueues <entity>.workflow.transitioned. Platform-internal: called from the
// workflow write path.
func EmitTransitioned(ctx context.Context, tx Tx, entity string, p Transitioned) error {
	p.Version = PayloadVersion
	return enqueue(ctx, tx, WorkflowTransitioned(entity), entity, p.RecordID, p)
}

// EmitRecord enqueues <entity>.record.<action>. Platform-internal: called from CRUD's write path.
func EmitRecord(ctx context.Context, tx Tx, entity string, action RecordAction, p RecordEvent) error {
	p.Version = PayloadVersion
	return enqueue(ctx, tx, Record(entity, action), entity, p.RecordID, p)
}

// EmitRaw enqueues a platform-owned topic with a payload the platform already built (the cron
// due event). It bypasses ValidateTopic's reserved-topic check; services use Emit.
func EmitRaw(ctx context.Context, tx Tx, topic, aggregateType string, aggregateID uuid.UUID, payload any) error {
	return enqueue(ctx, tx, topic, aggregateType, aggregateID, payload)
}
