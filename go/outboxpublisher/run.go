// Package outboxpublisher is the outbox pattern's read side — ported from metap's Rust
// metap-outbox-publisher crate. metap-infra's EnqueueOutboxEvent (the write side) writes a row
// into outbox_events inside the same transaction as a business write; this loop drains that
// table and publishes each row to an EventBus, so RabbitMQ downtime can't lose an event.
package outboxpublisher

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/metap-org/metap-go/infra/eventbus"
)

type outboxRow struct {
	id      uuid.UUID
	topic   string
	payload json.RawMessage
}

// Run runs the poll/publish/sleep loop until ctx is canceled, reconnecting the event bus with
// backoff after any publish failure.
//
// pool must be the same database EnqueueOutboxEvent actually wrote the rows into for whatever
// tenant this instance is meant to drain — see the Rust doc comment for the DedicatedDb-tenant
// gap this guards against (pointing this at the wrong pool silently drains nothing, forever).
func Run(ctx context.Context, pool *pgxpool.Pool, connect func() (eventbus.EventBus, error), pollMs uint64, batchSize int64) error {
	var attempt uint32

	bus, err := connectWithBackoff(ctx, connect, &attempt)
	if err != nil {
		return err // ctx canceled during initial connect
	}
	if bus == nil {
		return nil // shutdown during initial connect
	}

	for {
		select {
		case <-ctx.Done():
			slog.Info("shutdown signal received, exiting")
			_ = bus.Close()
			return nil
		default:
		}

		allPublished, err := publishPending(ctx, pool, bus, batchSize)
		if err != nil {
			// A DB-level failure (the transaction/query itself, not an individual row's
			// publish) isn't retried here either — crashes the process for a process manager
			// to restart, same contract as the Rust version.
			return err
		}

		if !allPublished {
			slog.Warn("publish failure detected, reconnecting to event bus")
			_ = bus.Close()
			attempt = 0
			bus, err = connectWithBackoff(ctx, connect, &attempt)
			if err != nil {
				return err
			}
			if bus == nil {
				return nil
			}
		}

		if !eventbus.SleepOrShutdown(ctx, time.Duration(pollMs)*time.Millisecond) {
			_ = bus.Close()
			return nil
		}
	}
}

func connectWithBackoff(ctx context.Context, connect func() (eventbus.EventBus, error), attempt *uint32) (eventbus.EventBus, error) {
	for {
		bus, err := connect()
		if err == nil {
			return bus, nil
		}
		slog.Warn("failed to connect to event bus, retrying", "error", err, "attempt", *attempt)
		if !eventbus.SleepOrShutdown(ctx, eventbus.BackoffDelay(*attempt)) {
			return nil, nil
		}
		*attempt++
	}
}

// publishPending: SELECT ... FOR UPDATE SKIP LOCKED held open for the whole
// publish-then-mark-done cycle, so concurrent workers skip rows this transaction has locked
// instead of double-publishing them. A per-row publish failure bumps attempts/last_error and
// leaves the row for the next poll cycle rather than failing the whole batch. Returns
// (false, nil) when at least one row failed to publish, so Run knows to reconnect the bus.
func publishPending(ctx context.Context, pool *pgxpool.Pool, bus eventbus.EventBus, batchSize int64) (bool, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)

	rows, err := tx.Query(ctx,
		"SELECT id, topic, payload FROM outbox_events WHERE published_at IS NULL ORDER BY created_at LIMIT $1 FOR UPDATE SKIP LOCKED",
		batchSize)
	if err != nil {
		return false, err
	}
	var batch []outboxRow
	for rows.Next() {
		var r outboxRow
		if err := rows.Scan(&r.id, &r.topic, &r.payload); err != nil {
			rows.Close()
			return false, err
		}
		batch = append(batch, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return false, err
	}

	allPublished := true
	for _, row := range batch {
		if pubErr := bus.Publish(ctx, row.topic, row.payload); pubErr == nil {
			if err := markPublished(ctx, tx, row.id); err != nil {
				return false, err
			}
		} else {
			slog.Warn("failed to publish outbox event, will retry next poll", "outbox_id", row.id, "topic", row.topic, "error", pubErr)
			if err := markFailed(ctx, tx, row.id, pubErr.Error()); err != nil {
				return false, err
			}
			allPublished = false
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return allPublished, nil
}

func markPublished(ctx context.Context, tx pgx.Tx, id uuid.UUID) error {
	_, err := tx.Exec(ctx, "UPDATE outbox_events SET published_at = now(), last_error = NULL WHERE id = $1", id)
	return err
}

func markFailed(ctx context.Context, tx pgx.Tx, id uuid.UUID, errMsg string) error {
	_, err := tx.Exec(ctx, "UPDATE outbox_events SET attempts = attempts + 1, last_error = $1 WHERE id = $2 AND published_at IS NULL",
		errMsg, id)
	return err
}
