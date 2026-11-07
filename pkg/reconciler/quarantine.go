// A per-entity table holding rows a migration couldn't transform, moved out *before* the batch
// transform runs (so the transform never hits them and never fails mid-batch). OriginalData is
// kept verbatim for a human to inspect/fix later (Resolve).
package reconciler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type QuarantineOutcome struct {
	Quarantined int64
	// SkippedReferenced: a candidate row that also has an incoming ON DELETE RESTRICT FK from
	// another entity's table couldn't be deleted out of the main table — Postgres itself
	// refused it, which is exactly the "don't quarantine a row still being referenced (avoid a
	// new orphan)" rule, enforced by the database rather than by this package tracking the
	// cross-entity FK graph itself.
	SkippedReferenced int64
}

func QuarantineTableName(table string) string {
	return table + "_quarantine"
}

func EnsureQuarantineTable(ctx context.Context, pool *pgxpool.Pool, table string) error {
	name := quoteQualifiedIdent(QuarantineTableName(table))
	_, err := pool.Exec(ctx, fmt.Sprintf(
		`CREATE TABLE IF NOT EXISTS %s (
		 id uuid PRIMARY KEY,
		 tenant_id uuid NOT NULL,
		 original_data jsonb NOT NULL,
		 migration_id text NOT NULL,
		 reason text NOT NULL,
		 detail jsonb,
		 quarantined_at timestamptz NOT NULL DEFAULT now(),
		 resolved_at timestamptz)`, name))
	return err
}

// QuarantineBadRows: badPredicate is a raw SQL boolean expression over the main table (aliased
// t) selecting which rows to move out — callers pass the *same* predicate preflight used to
// count them, so "how many are bad" and "which ones get quarantined" can never drift apart.
// Scoped to tenantID throughout, making the "one dedicated table per DedicatedDb tenant"
// convention structurally true for this operation rather than only documented.
func QuarantineBadRows(ctx context.Context, pool *pgxpool.Pool, tenantID uuid.UUID, table, badPredicate, migrationID, reason string) (QuarantineOutcome, error) {
	if err := EnsureQuarantineTable(ctx, pool, table); err != nil {
		return QuarantineOutcome{}, err
	}
	quotedTable := quoteQualifiedIdent(table)
	quotedQuarantine := quoteQualifiedIdent(QuarantineTableName(table))

	rows, err := pool.Query(ctx, fmt.Sprintf(
		"SELECT t.id FROM %s t WHERE t.tenant_id = $1 AND (%s)", quotedTable, badPredicate), tenantID)
	if err != nil {
		return QuarantineOutcome{}, err
	}
	var candidateIDs []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return QuarantineOutcome{}, err
		}
		candidateIDs = append(candidateIDs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return QuarantineOutcome{}, err
	}

	var outcome QuarantineOutcome
	for _, id := range candidateIDs {
		tx, err := pool.Begin(ctx)
		if err != nil {
			return outcome, err
		}

		var rowTenantID uuid.UUID
		var data json.RawMessage
		err = tx.QueryRow(ctx, fmt.Sprintf("SELECT tenant_id, data FROM %s WHERE id = $1 AND tenant_id = $2", quotedTable), id, tenantID).
			Scan(&rowTenantID, &data)
		if err != nil {
			tx.Rollback(ctx) //nolint:errcheck
			if errors.Is(err, pgx.ErrNoRows) {
				continue // already gone (concurrent write) — nothing to quarantine
			}
			return outcome, err
		}

		_, delErr := tx.Exec(ctx, fmt.Sprintf("DELETE FROM %s WHERE id = $1 AND tenant_id = $2", quotedTable), id, rowTenantID)
		if delErr != nil {
			tx.Rollback(ctx) //nolint:errcheck
			var pgErr *pgconn.PgError
			if errors.As(delErr, &pgErr) && pgErr.Code == "23503" {
				outcome.SkippedReferenced++
				continue
			}
			return outcome, delErr
		}

		_, err = tx.Exec(ctx, fmt.Sprintf(
			"INSERT INTO %s (id, tenant_id, original_data, migration_id, reason) VALUES ($1, $2, $3, $4, $5)",
			quotedQuarantine), id, rowTenantID, data, migrationID, reason)
		if err != nil {
			tx.Rollback(ctx) //nolint:errcheck
			return outcome, err
		}
		if err := tx.Commit(ctx); err != nil {
			return outcome, err
		}
		outcome.Quarantined++
	}
	return outcome, nil
}

// Resolve: a human (or a support tool) has fixed correctedData outside this package —
// re-inserts the row into the main table at its original id/tenantID and marks the quarantine
// row resolved. **Simplification**: this does not "replay the full transform chain" — this
// package has no persistent, queryable log of which MigrationOps have been applied historically
// to replay, so resolving means correctedData must already be valid for the table's *current*
// schema; the caller is responsible for that, not this function.
func Resolve(ctx context.Context, pool *pgxpool.Pool, table string, quarantineID uuid.UUID, correctedData json.RawMessage) error {
	quotedTable := quoteQualifiedIdent(table)
	quotedQuarantine := quoteQualifiedIdent(QuarantineTableName(table))
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var tenantID *uuid.UUID
	err = tx.QueryRow(ctx, fmt.Sprintf("SELECT tenant_id FROM %s WHERE id = $1 AND resolved_at IS NULL", quotedQuarantine), quarantineID).Scan(&tenantID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("quarantine row %s not found or already resolved", quarantineID)
		}
		return err
	}

	if _, err := tx.Exec(ctx, fmt.Sprintf("INSERT INTO %s (id, tenant_id, data) VALUES ($1, $2, $3)", quotedTable),
		quarantineID, tenantID, correctedData); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, fmt.Sprintf("UPDATE %s SET resolved_at = now() WHERE id = $1", quotedQuarantine), quarantineID); err != nil {
		return err
	}

	return tx.Commit(ctx)
}
