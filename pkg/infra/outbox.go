package infra

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

type OutboxEvent struct {
	Topic         string
	AggregateType string
	AggregateID   uuid.UUID
	Payload       json.RawMessage
}

type outboxExecutor interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// EnqueueOutboxEvent writes one outbox row in the same transaction as the business write — the
// write half of the outbox pattern. The drain/publish half is a separate worker.
func EnqueueOutboxEvent(ctx context.Context, executor outboxExecutor, event *OutboxEvent) error {
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
