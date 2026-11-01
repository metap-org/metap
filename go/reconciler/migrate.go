// Migration path for an entity ALREADY LIVE on a shared generic table that needs to move onto
// its own dedicated table — distinct from the normal table-per-entity path, which is about an
// entity created directly on a dedicated table from day one. Reconcile already builds/maintains
// the *target* table's structure for either case identically; this file adds the one thing that
// case doesn't need: moving rows that already exist on a *different* physical table onto it.
//
// Historical note (kept, not rewritten, per this port's own "faithful port" convention): the
// shared generic "records" table this module's source_table parameter was built for was dropped
// entirely by the Rust original's own migration 0033 — every entity in this codebase has been
// table-per-entity since. This module is therefore dead code in practice today, preserved as the
// one-time escape hatch it always was for a deployment that still has a legacy entity on it.
//
// Downtime-acceptable by design: no dual-write, no shadow-read. The caller is responsible for
// stopping the service that owns the entity before calling MigrateGenericToDedicated — this
// assumes no concurrent writer to sourceTable for the migrated (tenant, entity).
package reconciler

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/metap-org/metap-go/metadata"
)

const migrateBatchSize = 5000
const migrateThrottle = 20 * time.Millisecond

// MigrateOpID is the fixed op_id this file's checkpoint rows use in
// metadata.reconciler_backfill_progress — distinct from any real BackfillColumn op's own op_id
// (a field name) so the two kinds of progress row for the same (tenant, entity) can never
// collide.
const MigrateOpID = "migrate_generic_to_dedicated"

type CopySummary struct {
	// RowsScanned: rows the source query actually returned across every batch — advances the
	// resume cursor regardless of how many of them the destination's ON CONFLICT (id) DO
	// NOTHING accepted, so a resumed run can never loop forever re-fetching a batch it already
	// fully applied.
	RowsScanned int64
}

// CopyGenericRecords is a checkpointed batch copy of one (tenant, entity)'s rows from
// sourceTable (a generic table, filtered by its "entity" discriminator column) into destTable —
// keyset-paginated, same properties as RunBatchedUpdate.
func CopyGenericRecords(ctx context.Context, pool *pgxpool.Pool, tenantID uuid.UUID, entityName, sourceTable, destTable string) (CopySummary, error) {
	cursor, err := loadCursor(ctx, pool, tenantID, entityName, MigrateOpID)
	if err != nil {
		return CopySummary{}, err
	}

	quotedSource := quoteQualifiedIdent(sourceTable)
	quotedDest := quoteQualifiedIdent(destTable)
	entityLiteral := quoteLiteral(entityName)
	sql := fmt.Sprintf(
		`WITH batch AS (
			 SELECT id, tenant_id, code, status, data, version, deleted, created_at, updated_at, created_by, updated_by
			 FROM %s
			 WHERE tenant_id = $2 AND entity = %s AND id > $1
			 ORDER BY id LIMIT %d
		 ), ins AS (
			 INSERT INTO %s
			     (id, tenant_id, code, status, data, version, deleted, created_at, updated_at, created_by, updated_by)
			 SELECT id, tenant_id, code, status, data, version, deleted, created_at, updated_at, created_by, updated_by
			 FROM batch
			 ON CONFLICT (id) DO NOTHING
			 RETURNING id
		 )
		 SELECT b.id FROM batch b LEFT JOIN ins i ON i.id = b.id`,
		quotedSource, entityLiteral, migrateBatchSize, quotedDest)

	var rowsScanned int64
	for {
		tx, err := pool.Begin(ctx)
		if err != nil {
			return CopySummary{}, err
		}
		rows, err := tx.Query(ctx, sql, cursor, tenantID)
		if err != nil {
			tx.Rollback(ctx) //nolint:errcheck
			return CopySummary{}, err
		}
		var ids []uuid.UUID
		for rows.Next() {
			var id uuid.UUID
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				tx.Rollback(ctx) //nolint:errcheck
				return CopySummary{}, err
			}
			ids = append(ids, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			tx.Rollback(ctx) //nolint:errcheck
			return CopySummary{}, err
		}
		if len(ids) == 0 {
			if err := tx.Commit(ctx); err != nil {
				return CopySummary{}, err
			}
			break
		}

		rowsScanned += int64(len(ids))
		cursor = maxUUID(ids)
		if err := saveProgress(ctx, tx, tenantID, entityName, MigrateOpID, cursor, false); err != nil {
			tx.Rollback(ctx) //nolint:errcheck
			return CopySummary{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return CopySummary{}, err
		}
		time.Sleep(migrateThrottle)
	}

	if err := markCompleted(ctx, pool, tenantID, entityName, MigrateOpID); err != nil {
		return CopySummary{}, err
	}
	return CopySummary{RowsScanned: rowsScanned}, nil
}

type MigrateOutcome struct {
	// Table is the dedicated table's fully-qualified name — what a caller's own
	// EntityDefinition.TableName must be updated to before restarting the service.
	Table     string
	Reconcile ReconcileOutcome
	Copy      CopySummary
}

// MigrateGenericToDedicated is the full one-shot migration path: (a) Reconcile builds/updates
// entity's dedicated table exactly as it would for a brand-new table-per-entity entity, then (b)
// CopyGenericRecords moves every existing row for (tenant, entity) off sourceTable onto it. Two
// steps a caller still owns after this returns: flipping EntityDefinition.TableName in their own
// source code to QualifiedTableNameFor, and restarting the service.
func MigrateGenericToDedicated(ctx context.Context, pool *pgxpool.Pool, tenantID uuid.UUID, entity metadata.EntityDefinition, sourceTable string) (MigrateOutcome, error) {
	reconciled, err := Reconcile(ctx, pool, tenantID, entity, nil)
	if err != nil {
		return MigrateOutcome{}, err
	}
	copy, err := CopyGenericRecords(ctx, pool, tenantID, entity.Name, sourceTable, reconciled.Table)
	if err != nil {
		return MigrateOutcome{}, err
	}
	return MigrateOutcome{Table: reconciled.Table, Reconcile: reconciled, Copy: copy}, nil
}
