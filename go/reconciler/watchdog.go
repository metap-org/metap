// A CostHeavy op whose process died leaves the entity stuck Migrating forever (permanent 503s
// for it, per the design's Router-integration intent — not wired up yet). Lease + heartbeat
// (reconciler_entity_status.lease_expires_at, set by SetStatus) detects this; **never
// rollback** (DDL can't be rolled back once run) — requeue so the next Reconcile call picks up
// exactly where level-triggered reconciliation always resumes from (Introspect sees whatever
// state the dead process left behind and diffs from there), up to a retry cap past which the
// entity is marked Error instead of silently stuck.
package reconciler

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

const maxRetryAttempts = 5

type StuckEntity struct {
	TenantID   uuid.UUID
	EntityName string
	Attempts   int32
}

func FindStuckEntities(ctx context.Context, pool *pgxpool.Pool) ([]StuckEntity, error) {
	rows, err := pool.Query(ctx,
		`SELECT tenant_id, entity_name, attempts FROM metadata.reconciler_entity_status
		 WHERE status = 'migrating' AND lease_expires_at IS NOT NULL AND lease_expires_at < now()`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []StuckEntity
	for rows.Next() {
		var s StuckEntity
		if err := rows.Scan(&s.TenantID, &s.EntityName, &s.Attempts); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// RequeueOrFail clears the stale lease so a subsequent Reconcile call is free to re-acquire the
// advisory lock and pick the entity back up (requeue), unless Attempts is already past the
// retry cap — then it's marked Error instead, so a stuck entity surfaces as an alertable state
// rather than silently retrying forever.
func RequeueOrFail(ctx context.Context, pool *pgxpool.Pool, stuck StuckEntity) error {
	if stuck.Attempts+1 > maxRetryAttempts {
		_, err := pool.Exec(ctx,
			`UPDATE metadata.reconciler_entity_status SET status = 'error', attempts = attempts + 1,
			 last_error = $3, lease_owner = NULL, lease_expires_at = NULL, updated_at = now()
			 WHERE tenant_id = $1 AND entity_name = $2`,
			stuck.TenantID, stuck.EntityName,
			fmt.Sprintf("watchdog: exceeded %d retry attempts on a stuck Migrating lease — needs manual intervention", maxRetryAttempts))
		return err
	}
	_, err := pool.Exec(ctx,
		`UPDATE metadata.reconciler_entity_status SET attempts = attempts + 1, lease_owner = NULL,
		 lease_expires_at = NULL, updated_at = now() WHERE tenant_id = $1 AND entity_name = $2`,
		stuck.TenantID, stuck.EntityName)
	return err
}
