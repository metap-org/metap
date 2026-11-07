// Checkpointed batch backfill, shared by two callers: executor (populating a newly promoted
// storage:column field from data ->> sourceField) and migration (transforming data in place for
// a widen_type op). Keyset pagination (id > cursor, no OFFSET); the checkpoint is saved in the
// *same transaction* as the batch update (atomic — a crash between them is impossible, so resume
// is always exactly where the last committed batch left off); a small sleep between batches so
// this never starves autovacuum/replication; cancellable is left to the caller.
package reconciler

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const backfillBatchSize = 5000
const backfillThrottle = 20 * time.Millisecond

// BackfillScope: whether the rows a backfill touches for one (tenantID, entity) reconcile belong
// exclusively to tenantID, or are spread across every tenant sharing one physical table. This
// package has no concept of a tenant strategy itself (that's control, a layer up) — the caller
// supplies this because only it knows which shape applies.
type BackfillScope int

const (
	// ScopeSingleTenant: every row this reconcile's table holds belongs to tenantID — a
	// DedicatedDb tenant's own database, or any other reconcile genuinely invoked per real
	// tenant. The historical, still-correct default.
	ScopeSingleTenant BackfillScope = iota
	// ScopeAllTenants: the table is shared across many tenants and tenantID is a sentinel
	// identifying *this reconcile call*, not a row owner to filter by (control.PlatformTenantID
	// at a Schema-strategy service's own boot). A backfill scoped this way touches every row in
	// the table regardless of which real tenant it belongs to; the progress ledger still keys
	// off tenantID as given, representing "this whole shared table's backfill", not any one
	// tenant's.
	ScopeAllTenants
)

func RunHeavyBackfill(ctx context.Context, pool *pgxpool.Pool, tenantID uuid.UUID, entityName, table, opID, column, sourceField, sqlType string, scope BackfillScope) error {
	quotedCol := quoteIdent(column)
	fieldLiteral := quoteLiteral(sourceField)
	setClause := fmt.Sprintf("%s = (t.data ->> %s)::%s", quotedCol, fieldLiteral, sqlType)
	return RunBatchedUpdate(ctx, pool, tenantID, entityName, table, opID, setClause, "", scope)
}

// RunBatchedUpdate: setClause is a raw "SET ..." fragment (already valid SQL — callers build it
// from server-authored metadata only). whereExtra, if non-empty, further restricts which rows a
// batch picks up — used by migration's widenType with an idempotent predicate so a resumed/re-run
// pass only touches rows that still need the transform.
func RunBatchedUpdate(ctx context.Context, pool *pgxpool.Pool, tenantID uuid.UUID, entityName, table, opID, setClause, whereExtra string, scope BackfillScope) error {
	cursor, err := loadCursor(ctx, pool, tenantID, entityName, opID)
	if err != nil {
		return err
	}

	quotedTable := quoteQualifiedIdent(table)
	extra := ""
	if whereExtra != "" {
		extra = fmt.Sprintf(" AND (%s)", whereExtra)
	}

	// t.tenant_id = $2 scoping (SingleTenant) makes the "every dedicated table belongs to
	// exactly one DedicatedDb tenant" convention structurally true rather than merely
	// documented. ScopeAllTenants drops the filter entirely instead of binding it to a sentinel
	// tenant_id that owns zero real rows — both branches still key the progress ledger off
	// tenantID as given, unrelated to what the query itself scans.
	var sql string
	switch scope {
	case ScopeAllTenants:
		sql = fmt.Sprintf(
			`WITH batch AS (SELECT id FROM %s t WHERE id > $1%s ORDER BY id LIMIT %d)
			 UPDATE %s t SET %s FROM batch WHERE t.id = batch.id RETURNING t.id`,
			quotedTable, extra, backfillBatchSize, quotedTable, setClause)
	default:
		sql = fmt.Sprintf(
			`WITH batch AS (SELECT id FROM %s t WHERE t.tenant_id = $2 AND id > $1%s ORDER BY id LIMIT %d)
			 UPDATE %s t SET %s FROM batch WHERE t.id = batch.id RETURNING t.id`,
			quotedTable, extra, backfillBatchSize, quotedTable, setClause)
	}

	for {
		tx, err := pool.Begin(ctx)
		if err != nil {
			return err
		}
		var rows pgx.Rows
		if scope == ScopeAllTenants {
			rows, err = tx.Query(ctx, sql, cursor)
		} else {
			rows, err = tx.Query(ctx, sql, cursor, tenantID)
		}
		if err != nil {
			tx.Rollback(ctx) //nolint:errcheck
			return err
		}
		var ids []uuid.UUID
		for rows.Next() {
			var id uuid.UUID
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				tx.Rollback(ctx) //nolint:errcheck
				return err
			}
			ids = append(ids, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			tx.Rollback(ctx) //nolint:errcheck
			return err
		}
		if len(ids) == 0 {
			if err := tx.Commit(ctx); err != nil {
				return err
			}
			break
		}
		cursor = maxUUID(ids)
		if err := saveProgress(ctx, tx, tenantID, entityName, opID, cursor, false); err != nil {
			tx.Rollback(ctx) //nolint:errcheck
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		time.Sleep(backfillThrottle)
	}

	return markCompleted(ctx, pool, tenantID, entityName, opID)
}

// maxUUID returns the largest UUID by byte order — matches Rust's Uuid::Ord (the derived
// comparison over the 16-byte array), which the original relies on via ids.iter().max().
func maxUUID(ids []uuid.UUID) uuid.UUID {
	max := ids[0]
	for _, id := range ids[1:] {
		if bytes.Compare(id[:], max[:]) > 0 {
			max = id
		}
	}
	return max
}

// loadCursor is reused as-is by migrate.go's copyGenericRecords, which needs the exact same
// checkpoint-table shape for a different kind of op.
func loadCursor(ctx context.Context, pool *pgxpool.Pool, tenantID uuid.UUID, entityName, opID string) (uuid.UUID, error) {
	var cursor *uuid.UUID
	err := pool.QueryRow(ctx,
		"SELECT cursor_id FROM metadata.reconciler_backfill_progress WHERE tenant_id = $1 AND entity_name = $2 AND op_id = $3",
		tenantID, entityName, opID).Scan(&cursor)
	if err != nil {
		if err == pgx.ErrNoRows {
			return uuid.Nil, nil
		}
		return uuid.Nil, err
	}
	if cursor == nil {
		return uuid.Nil, nil
	}
	return *cursor, nil
}

func saveProgress(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, entityName, opID string, cursor uuid.UUID, completed bool) error {
	_, err := tx.Exec(ctx,
		`INSERT INTO metadata.reconciler_backfill_progress (tenant_id, entity_name, op_id, cursor_id, completed, updated_at)
		 VALUES ($1, $2, $3, $4, $5, now())
		 ON CONFLICT (tenant_id, entity_name, op_id) DO UPDATE
		 SET cursor_id = EXCLUDED.cursor_id, completed = EXCLUDED.completed, updated_at = now()`,
		tenantID, entityName, opID, cursor, completed)
	return err
}

func markCompleted(ctx context.Context, pool *pgxpool.Pool, tenantID uuid.UUID, entityName, opID string) error {
	_, err := pool.Exec(ctx,
		`INSERT INTO metadata.reconciler_backfill_progress (tenant_id, entity_name, op_id, completed, updated_at)
		 VALUES ($1, $2, $3, true, now())
		 ON CONFLICT (tenant_id, entity_name, op_id) DO UPDATE SET completed = true, updated_at = now()`,
		tenantID, entityName, opID)
	return err
}
