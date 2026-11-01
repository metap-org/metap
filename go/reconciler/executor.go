// Runs a planned []DdlOp against Postgres: an advisory lock so two reconciles for the same
// (tenant, entity) never run concurrently, Migrating status while any CostHeavy op is in flight,
// each op dispatched by its ExecutionMode.
//
// Not this package's job: persisting the new EntityDefinition into whatever registry the caller
// uses. "Only store the registry AFTER DDL is done" is an invariant for the *caller* to honor —
// Reconcile only returns nil after every op below has actually run; a caller must not treat an
// entity's new field/index as live until that happens.
package reconciler

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ensureSchemaExists: CREATE SCHEMA IF NOT EXISTS is not actually safe under real concurrency —
// Postgres's existence check and the creation itself aren't atomic together. 42710
// (duplicate_object) or 23505 (unique_violation on pg_namespace's own index, the race's actual
// shape) both mean "the schema exists now", which is all this function promises.
func ensureSchemaExists(ctx context.Context, pool *pgxpool.Pool, schema string) error {
	_, err := pool.Exec(ctx, "CREATE SCHEMA IF NOT EXISTS "+quoteIdent(schema))
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && (pgErr.Code == "42710" || pgErr.Code == "23505") {
		return nil
	}
	return err
}

// Execute is the historical signature, unchanged — every existing caller gets
// ScopeSingleTenant, correct for a genuinely per-tenant reconcile (DedicatedDb, or any call
// already scoped to one real tenant). A caller reconciling a Schema-strategy shared table with a
// sentinel tenantID needs ExecuteWithScope instead.
func Execute(ctx context.Context, pool *pgxpool.Pool, tenantID uuid.UUID, entityName string, desired PhysicalSchema, ops []DdlOp) error {
	return ExecuteWithScope(ctx, pool, tenantID, entityName, desired, ops, ScopeSingleTenant)
}

// ExecuteWithScope: see BackfillScope's own doc comment for what scope changes and why it
// exists — everything else about this function is identical to Execute.
func ExecuteWithScope(ctx context.Context, pool *pgxpool.Pool, tenantID uuid.UUID, entityName string, desired PhysicalSchema, ops []DdlOp, scope BackfillScope) error {
	if len(ops) == 0 {
		return nil
	}

	if schema, _, ok := strings.Cut(desired.Table, "."); ok {
		if err := ensureSchemaExists(ctx, pool, schema); err != nil {
			return err
		}
	}

	// A dedicated connection held for the lock's entire lifetime — see TryAdvisoryLock's doc
	// comment for why this must not be a bare pool call. The actual DDL/backfill work below
	// still goes through pool freely; only the lock itself needs this one pinned connection.
	lockConn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer lockConn.Release()

	lockOwner := uuid.New()
	acquired, err := TryAdvisoryLock(ctx, lockConn, tenantID, entityName)
	if err != nil {
		return err
	}
	if !acquired {
		return fmt.Errorf("another reconcile is already running for %s", entityName)
	}
	result := executeLocked(ctx, pool, tenantID, entityName, desired, ops, lockOwner, scope)
	if err := AdvisoryUnlock(ctx, lockConn, tenantID, entityName); err != nil {
		return err
	}
	return result
}

func executeLocked(ctx context.Context, pool *pgxpool.Pool, tenantID uuid.UUID, entityName string, desired PhysicalSchema, ops []DdlOp, lockOwner uuid.UUID, scope BackfillScope) error {
	hasHeavy := false
	for _, op := range ops {
		if op.Cost() == CostHeavy {
			hasHeavy = true
			break
		}
	}
	if hasHeavy {
		if err := SetStatus(ctx, pool, tenantID, entityName, StatusMigrating, lockOwner, 10*time.Minute); err != nil {
			return err
		}
	}

	for _, op := range ops {
		if err := runOne(ctx, pool, tenantID, entityName, desired, op, scope); err != nil {
			if recErr := RecordError(ctx, pool, tenantID, entityName, fmt.Sprintf("%v", err)); recErr != nil {
				return recErr
			}
			return err
		}
	}

	if hasHeavy {
		if err := SetStatus(ctx, pool, tenantID, entityName, StatusActive, lockOwner, 0); err != nil {
			return err
		}
	}
	return nil
}

func runOne(ctx context.Context, pool *pgxpool.Pool, tenantID uuid.UUID, entityName string, desired PhysicalSchema, op DdlOp, scope BackfillScope) error {
	switch op.ExecutionMode() {
	case ModeTransactional:
		tx, err := pool.Begin(ctx)
		if err != nil {
			return err
		}
		for _, stmt := range buildSQL(desired.Table, op) {
			if _, err := tx.Exec(ctx, stmt); err != nil {
				tx.Rollback(ctx) //nolint:errcheck
				return err
			}
		}
		return tx.Commit(ctx)
	case ModeNonTransactional:
		// CREATE/DROP INDEX CONCURRENTLY errors inside a transaction block — a plain pool.Exec
		// does not wrap in one.
		for _, stmt := range buildSQL(desired.Table, op) {
			if _, err := pool.Exec(ctx, stmt); err != nil {
				return err
			}
		}
		return nil
	default: // ModeBatched
		if op.Kind != OpBackfillColumn {
			panic("reconciler: only BackfillColumn is Batched")
		}
		return RunHeavyBackfill(ctx, pool, tenantID, entityName, desired.Table, op.BackfillOpID, op.BackfillColumn, op.BackfillSourceField, op.BackfillSQLType, scope)
	}
}

// buildSQL builds the SQL statement(s) for one DdlOp. Multiple statements only for
// AddSyncTrigger (function + trigger). Exported (not just package-private) so plan.go can reuse
// it for a DB-free "what would Execute run" preview without duplicating this switch — Execute
// itself is still the only caller that actually runs the returned SQL against Postgres.
func buildSQL(table string, op DdlOp) []string {
	t := quoteQualifiedIdent(table)
	switch op.Kind {
	case OpCreateTable:
		// The table's schema (ensureSchemaExists, called once per Execute before any op runs)
		// must already exist by the time this runs.
		return []string{fmt.Sprintf(
			`CREATE TABLE IF NOT EXISTS %s (
			 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
			 tenant_id uuid NOT NULL,
			 code varchar(120),
			 status varchar(80),
			 data jsonb NOT NULL DEFAULT '{}'::jsonb,
			 version integer NOT NULL DEFAULT 1,
			 deleted boolean NOT NULL DEFAULT false,
			 created_at timestamptz NOT NULL DEFAULT now(),
			 updated_at timestamptz NOT NULL DEFAULT now(),
			 created_by uuid,
			 updated_by uuid)`, t)}
	case OpAddColumn:
		return []string{fmt.Sprintf("ALTER TABLE %s ADD COLUMN IF NOT EXISTS %s %s", t, quoteIdent(op.ColumnName), op.ColumnSpec.SQLType)}
	case OpRenameColumn:
		return []string{fmt.Sprintf("ALTER TABLE %s RENAME COLUMN %s TO %s", t, quoteIdent(op.RenameFrom), quoteIdent(op.RenameTo))}
	case OpAddSyncTrigger:
		return buildSyncTriggerSQL(table, op.SyncField, op.SyncSQLType)
	case OpBackfillColumn:
		return nil // Batched — handled by runOne directly, never via SQL text.
	case OpCreateIndexConcurrently:
		return []string{buildCreateIndexSQL(table, op.IndexName, op.IndexSpec)}
	case OpDropIndexConcurrently:
		return []string{fmt.Sprintf("DROP INDEX CONCURRENTLY IF EXISTS %s", quoteIdent(op.IndexName))}
	case OpAddForeignKeyNotValid:
		return []string{buildAddFkSQL(table, op.FkName, op.FkSpec)}
	case OpValidateForeignKey:
		return []string{fmt.Sprintf("ALTER TABLE %s VALIDATE CONSTRAINT %s", t, quoteIdent(op.FkName))}
	case OpDropForeignKey:
		return []string{fmt.Sprintf("ALTER TABLE %s DROP CONSTRAINT IF EXISTS %s", t, quoteIdent(op.FkName))}
	case OpAddUnique:
		return []string{buildAddUniqueSQL(table, op.UniqueName, op.UniqueSpec)}
	case OpDropUnique:
		return []string{fmt.Sprintf("ALTER TABLE %s DROP CONSTRAINT IF EXISTS %s", t, quoteIdent(op.UniqueName))}
	default:
		return nil
	}
}

func buildCreateIndexSQL(table, name string, spec IndexSpec) string {
	unique := ""
	if spec.Unique {
		unique = "UNIQUE "
	}
	using := ""
	if spec.Using != "" {
		using = "USING " + spec.Using + " "
	}
	// WhereClause is always a fixed string this package itself generates (Compile's "deleted =
	// false"), never request/tenant input — safe to inline unquoted.
	whereClause := ""
	if spec.WhereClause != "" {
		whereClause = " WHERE " + spec.WhereClause
	}
	return fmt.Sprintf("CREATE %sINDEX CONCURRENTLY IF NOT EXISTS %s ON %s %s(%s)%s",
		unique, quoteIdent(name), quoteQualifiedIdent(table), using, spec.Expression, whereClause)
}

func buildAddFkSQL(table, name string, spec FkSpec) string {
	return fmt.Sprintf("ALTER TABLE %s ADD CONSTRAINT %s FOREIGN KEY (%s) REFERENCES %s (%s) ON DELETE %s NOT VALID",
		quoteQualifiedIdent(table), quoteIdent(name), quoteIdent(spec.Column), quoteQualifiedIdent(spec.RefTable), quoteIdent(spec.RefColumn), spec.OnDelete.SQL())
}

func buildAddUniqueSQL(table, name string, spec UniqueSpec) string {
	cols := make([]string, len(spec.Columns))
	for i, c := range spec.Columns {
		cols[i] = quoteIdent(c)
	}
	return fmt.Sprintf("ALTER TABLE %s ADD CONSTRAINT %s UNIQUE (%s)", quoteQualifiedIdent(table), quoteIdent(name), strings.Join(cols, ", "))
}

// buildSyncTriggerSQL: the trigger function name is derived from (table, field) so two different
// promoted fields on the same table never collide, and re-running this op (crash-resume) is a
// plain CREATE OR REPLACE. Uses the *bare* table name (after the last ".") for the
// function/trigger name itself — same convention indexName already uses.
func buildSyncTriggerSQL(table, field, sqlType string) []string {
	bareTable := table
	if idx := strings.LastIndexByte(table, '.'); idx >= 0 {
		bareTable = table[idx+1:]
	}
	fnName := quoteIdent(fmt.Sprintf("sync_%s_%s", bareTable, field))
	triggerName := quoteIdent(fmt.Sprintf("trg_sync_%s_%s", bareTable, field))
	column := quoteIdent(field)
	fieldLiteral := quoteLiteral(field)
	return []string{
		fmt.Sprintf(`CREATE OR REPLACE FUNCTION %s() RETURNS trigger AS $$ BEGIN NEW.%s := (NEW.data ->> %s)::%s; RETURN NEW; END; $$ LANGUAGE plpgsql`,
			fnName, column, fieldLiteral, sqlType),
		fmt.Sprintf(`CREATE OR REPLACE TRIGGER %s BEFORE INSERT OR UPDATE ON %s FOR EACH ROW EXECUTE FUNCTION %s()`,
			triggerName, quoteQualifiedIdent(table), fnName),
	}
}
