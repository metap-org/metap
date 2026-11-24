package outbox

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

// AuditTopic is the topic of an audit-trail entry written to the outbox in its business
// transaction. It is never published to the broker: the audit drain (pkg/audit) takes those rows
// into the tenant's hash chain, and the publisher leaves them alone.
const AuditTopic = "audit.entry"

type Event struct {
	Topic         string
	AggregateType string
	AggregateID   uuid.UUID
	Payload       json.RawMessage
}

type outboxExecutor interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// Enqueue writes one outbox row in the same transaction as the business write — the
// write half of the outbox pattern. The drain/publish half is a separate worker.
func Enqueue(ctx context.Context, executor outboxExecutor, event *Event) error {
	// Bare table name, deliberately not schema-qualified — relies on the caller's connection
	// search_path already including "metadata" (Router.Begin's convention).
	_, err := executor.Exec(ctx,
		"INSERT INTO outbox_events (topic, aggregate_type, aggregate_id, payload) VALUES ($1, $2, $3, $4)",
		event.Topic, event.AggregateType, event.AggregateID, event.Payload)
	if err != nil {
		return err
	}
	slog.Debug("outbox event enqueued", "topic", event.Topic, "aggregate_id", event.AggregateID)
	return nil
}
