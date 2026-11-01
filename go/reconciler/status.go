// Per-(tenant, entity) reconcile status (metadata.reconciler_entity_status) — what the executor
// flips to Migrating before running a Cost::Heavy op and back to Active once done, and what the
// watchdog scans for entities stuck past their lease.
package reconciler

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type EntityStatus int

const (
	StatusActive EntityStatus = iota
	StatusMigrating
	StatusError
)

func (s EntityStatus) String() string {
	switch s {
	case StatusMigrating:
		return "migrating"
	case StatusError:
		return "error"
	default:
		return "active"
	}
}

func GetStatus(ctx context.Context, pool *pgxpool.Pool, tenantID uuid.UUID, entityName string) (EntityStatus, error) {
	var row *string
	err := pool.QueryRow(ctx,
		"SELECT status FROM metadata.reconciler_entity_status WHERE tenant_id = $1 AND entity_name = $2",
		tenantID, entityName).Scan(&row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return StatusActive, nil
		}
		return StatusActive, err
	}
	if row == nil {
		return StatusActive, nil
	}
	switch *row {
	case "migrating":
		return StatusMigrating, nil
	case "error":
		return StatusError, nil
	default:
		return StatusActive, nil
	}
}

// SetStatus sets status and, for Migrating, claims a fresh lease (leaseOwner/leaseExpiresAt) —
// what the watchdog checks to tell "actively being worked on" from "died mid-op". leaseTTL
// should be comfortably longer than a single DdlOp's expected run time.
func SetStatus(ctx context.Context, pool *pgxpool.Pool, tenantID uuid.UUID, entityName string, status EntityStatus, leaseOwner uuid.UUID, leaseTTL time.Duration) error {
	var leaseExpiresAt *time.Time
	if status == StatusMigrating {
		t := time.Now().Add(leaseTTL)
		leaseExpiresAt = &t
	}
	_, err := pool.Exec(ctx,
		`INSERT INTO metadata.reconciler_entity_status (tenant_id, entity_name, status, lease_owner, lease_expires_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, now())
		 ON CONFLICT (tenant_id, entity_name) DO UPDATE
		 SET status = EXCLUDED.status, lease_owner = EXCLUDED.lease_owner,
		     lease_expires_at = EXCLUDED.lease_expires_at, updated_at = now()`,
		tenantID, entityName, status.String(), leaseOwner, leaseExpiresAt)
	return err
}

func RecordError(ctx context.Context, pool *pgxpool.Pool, tenantID uuid.UUID, entityName, errMsg string) error {
	_, err := pool.Exec(ctx,
		`INSERT INTO metadata.reconciler_entity_status (tenant_id, entity_name, status, attempts, last_error, updated_at)
		 VALUES ($1, $2, 'error', 1, $3, now())
		 ON CONFLICT (tenant_id, entity_name) DO UPDATE
		 SET status = 'error', attempts = metadata.reconciler_entity_status.attempts + 1,
		     last_error = EXCLUDED.last_error, updated_at = now()`,
		tenantID, entityName, errMsg)
	return err
}

// TryAdvisoryLock: pg_try_advisory_lock is **session-scoped** — it locks against whichever
// physical connection issues it, and only the *same* connection can release it. conn must be one
// connection acquired via pool.Acquire and held for the lock's entire lifetime, separate from
// whatever connections the actual DDL/backfill work checks out from the pool.
//
// hashtext is Postgres's own stable string hash, so two callers hashing the same (tenantID,
// entityName) pair always contend for the same lock key, unlike hashing client-side with a
// generic hasher whose output isn't guaranteed stable across process restarts.
func TryAdvisoryLock(ctx context.Context, conn *pgxpool.Conn, tenantID uuid.UUID, entityName string) (bool, error) {
	key := fmt.Sprintf("%s:%s", tenantID, entityName)
	var acquired bool
	err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock(hashtext($1)::bigint)", key).Scan(&acquired)
	return acquired, err
}

func AdvisoryUnlock(ctx context.Context, conn *pgxpool.Conn, tenantID uuid.UUID, entityName string) error {
	key := fmt.Sprintf("%s:%s", tenantID, entityName)
	_, err := conn.Exec(ctx, "SELECT pg_advisory_unlock(hashtext($1)::bigint)", key)
	return err
}
