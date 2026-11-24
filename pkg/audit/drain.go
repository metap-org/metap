package audit

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/metap-org/metap/pkg/kit/standard"
)

// Recorder is what the drain writes entries into; PostgresStore is the production one. RecordOnce
// must be idempotent per id: the drain may deliver a row twice.
type Recorder interface {
	RecordOnce(ctx context.Context, tenantID, id uuid.UUID, entry Entry) error
}

// Drain moves up to batch pending audit entries from pool's outbox into rec and marks them done,
// and reports how many it handled. It holds no database connection while rec writes (rec has its
// own transaction, usually on the same pool), so drains never wait on each other for one; two
// drains may both pick a row, and RecordOnce makes the second a no-op.
//
// A row that cannot be understood is marked done with its error kept in last_error rather than
// retried forever; a row rec fails on stays pending with attempts raised, and is retried.
func Drain(ctx context.Context, pool *pgxpool.Pool, rec Recorder, batch int) (int, error) {
	rows, err := pool.Query(ctx,
		`SELECT id, payload FROM metadata.outbox_events WHERE published_at IS NULL AND topic = $1 ORDER BY created_at, id LIMIT $2`,
		Topic, batch)
	if err != nil {
		return 0, err
	}
	type pendingRow struct {
		id      uuid.UUID
		payload []byte
	}
	var batchRows []pendingRow
	for rows.Next() {
		var r pendingRow
		if err := rows.Scan(&r.id, &r.payload); err != nil {
			rows.Close()
			return 0, err
		}
		batchRows = append(batchRows, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	handled := 0
	for _, r := range batchRows {
		entry, err := decodePending(r.payload)
		if err != nil {
			slog.Error("dropping an audit entry that cannot be decoded", "outbox_id", r.id, standard.LogError, err)
			if _, err := pool.Exec(ctx, `UPDATE metadata.outbox_events SET published_at = now(), last_error = $2 WHERE id = $1`, r.id, "undecodable: "+err.Error()); err != nil {
				return handled, err
			}
			continue
		}
		if err := rec.RecordOnce(ctx, entry.TenantID, r.id, entry); err != nil {
			slog.Error("failed to record audit entry, will retry", "outbox_id", r.id, "entity", entry.Entity, standard.LogError, err)
			if _, err := pool.Exec(ctx, `UPDATE metadata.outbox_events SET attempts = attempts + 1, last_error = $2 WHERE id = $1`, r.id, err.Error()); err != nil {
				return handled, err
			}
			continue
		}
		if _, err := pool.Exec(ctx, `UPDATE metadata.outbox_events SET published_at = now(), last_error = NULL WHERE id = $1`, r.id); err != nil {
			return handled, err
		}
		handled++
	}
	return handled, nil
}

// DrainAll drains pool until nothing is pending (or ctx ends); for tests and one-shot tools.
func DrainAll(ctx context.Context, pool *pgxpool.Pool, rec Recorder) error {
	for {
		n, err := Drain(ctx, pool, rec, 100)
		if err != nil {
			return err
		}
		if n == 0 {
			return nil
		}
	}
}

// RunDrain polls every pool pools returns (the shared one, plus a dedicated database per tenant
// that has one) until ctx ends. A full batch is followed at once by the next; otherwise it waits
// poll. A failure of one pool is logged and the others carry on.
func RunDrain(ctx context.Context, pools func(context.Context) ([]*pgxpool.Pool, error), rec Recorder, poll time.Duration, batch int) {
	for {
		busy := false
		list, err := pools(ctx)
		if err != nil {
			slog.Error("listing the databases to drain audit entries from", standard.LogError, err)
		}
		for _, p := range list {
			n, err := Drain(ctx, p, rec, batch)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				slog.Error("draining audit entries", standard.LogError, err)
				continue
			}
			if n >= batch {
				busy = true
			}
		}
		if busy {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(poll):
		}
	}
}
