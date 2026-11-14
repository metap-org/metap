// Data Plane Data Evolution. Diff/Execute already cover plain add/rename (a RenameColumn/
// AddColumn DdlOp never fails against existing data). What's left is the op that *can* fail
// against dirty data — widening a field's type — plus the declarative, no-code-hook op set. No
// custom transform functions: every op here maps to a fixed, generated SQL statement, never
// arbitrary caller code.
package reconciler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// scalarFallbackText is the fallback's *raw* text, for interpolating into a scalar SQL cast
// (::text/::date/...) — distinct from a JSON rendering, which a plain string value would wrap
// in quote marks. applySQL's WidenType Coerce fallback needs the bare text: its target is a
// scalar cast, not ::jsonb.
func scalarFallbackText(raw json.RawMessage) (string, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return "", err
	}
	switch vv := v.(type) {
	case string:
		return vv, nil
	case bool:
		if vv {
			return "true", nil
		}
		return "false", nil
	case json.Number:
		return vv.String(), nil
	default:
		return "", fmt.Errorf("WidenType coerce fallback must be a string, bool, or number, got %s", raw)
	}
}

type MigrationOpKind int

const (
	MigRenameField MigrationOpKind = iota
	MigAddField
	// MigWidenType is the one op that can fail against existing data — ToSQLType must be a type
	// pg_input_is_valid (Postgres 16+) understands, e.g. "numeric(18,4)", "timestamptz". Assumes
	// the field's *current* JSON representation is a string (jsonb_typeof = 'string').
	MigWidenType
	MigDropField
	MigRemoveEnum
)

// MigrationOp mirrors the Rust MigrationOp enum.
type MigrationOp struct {
	Kind MigrationOpKind

	// RenameField
	RenameFrom, RenameTo string

	// AddField. Default == nil leaves the field simply absent on pre-existing rows (lazy — the
	// next write populates it); required-with-no-default is a metadata-authoring-time
	// validation concern, not something this package re-checks.
	AddFieldName    string
	AddFieldDefault json.RawMessage

	// WidenType
	WidenField     string
	WidenToSQLType string

	// DropField
	DropField    string
	DropKeepData bool

	// RemoveEnum
	RemoveEnumField   string
	RemoveEnumValue   string
	RemoveEnumRemapTo string
}

type QuarantinePolicyKind int

const (
	// PolicyBlock is the default — halt, do nothing to the data, and let the caller surface
	// BadRows to a human before retrying.
	PolicyBlock QuarantinePolicyKind = iota
	// PolicyCoerce: every row gets Fallback instead of failing the cast — no row is ever
	// removed. Fallback must be a JSON value already of the *target* scalar type (a JSON
	// number for a WidenType targeting numeric, not a JSON string of digits) — it is embedded
	// directly into a ::{ToSQLType} cast, not round-tripped through jsonb first.
	PolicyCoerce
	// PolicyQuarantine moves bad rows to {table}_quarantine before the transform runs, so the
	// transform itself only ever touches clean rows.
	PolicyQuarantine
)

type QuarantinePolicy struct {
	Kind           QuarantinePolicyKind
	CoerceFallback json.RawMessage // PolicyCoerce only
}

type PreflightReport struct {
	BadRows int64
}

// Preflight: "scan-count BEFORE migrating, don't blow up partway through 10M rows" — a pure
// SELECT count(*), no mutation. Only MigWidenType can ever report BadRows > 0; every other op is
// safe by construction (rename/add/drop never fail a cast, RemoveEnum always has an explicit
// RemoveEnumRemapTo).
func Preflight(ctx context.Context, pool *pgxpool.Pool, tenantID uuid.UUID, table string, op MigrationOp) (PreflightReport, error) {
	predicate, ok := badRowPredicate(op)
	if !ok {
		return PreflightReport{}, nil
	}
	quotedTable := quoteQualifiedIdent(table)
	// t.tenant_id = $1 — scoped so a count across a table shared by more than one tenant
	// (never supposed to happen) doesn't report a number that includes rows this migration will
	// never actually touch.
	var badRows int64
	err := pool.QueryRow(ctx, fmt.Sprintf("SELECT count(*) FROM %s t WHERE t.tenant_id = $1 AND (%s)", quotedTable, predicate), tenantID).Scan(&badRows)
	if err != nil {
		return PreflightReport{}, err
	}
	return PreflightReport{BadRows: badRows}, nil
}

// PreflightFkOrphans is the "orphan ref" preflight — separate from MigrationOp because it
// belongs to a FkSpec (a Reference field's constraint), not a data-evolution op.
func PreflightFkOrphans(ctx context.Context, pool *pgxpool.Pool, table string, fk FkSpec) (PreflightReport, error) {
	predicate := fkOrphanPredicate(fk)
	quotedTable := quoteQualifiedIdent(table)
	var badRows int64
	err := pool.QueryRow(ctx, fmt.Sprintf("SELECT count(*) FROM %s t WHERE %s", quotedTable, predicate)).Scan(&badRows)
	if err != nil {
		return PreflightReport{}, err
	}
	return PreflightReport{BadRows: badRows}, nil
}

func fkOrphanPredicate(fk FkSpec) string {
	col := quoteIdent(fk.Column)
	refTable := quoteQualifiedIdent(fk.RefTable)
	refCol := quoteIdent(fk.RefColumn)
	return fmt.Sprintf("t.%s IS NOT NULL AND NOT EXISTS (SELECT 1 FROM %s r WHERE r.%s = t.%s)", col, refTable, refCol, col)
}

func badRowPredicate(op MigrationOp) (string, bool) {
	if op.Kind != MigWidenType {
		return "", false
	}
	col := quoteIdent(op.WidenField)
	ty := quoteLiteral(op.WidenToSQLType)
	return fmt.Sprintf("t.%s IS NOT NULL AND NOT pg_input_is_valid(t.%s::text, %s)", col, col, ty), true
}

type MigrationOutcome struct {
	Quarantined       int64
	SkippedReferenced int64
}

// RunMigration runs one MigrationOp end to end: preflight -> apply policy -> batched transform
// (only the rows still needing it — idempotent, safe to call again). migrationID identifies
// this run in metadata.reconciler_backfill_progress/quarantine rows (checkpoint/resume key) —
// callers should pass something stable across retries of the *same* logical migration.
func RunMigration(ctx context.Context, pool *pgxpool.Pool, tenantID uuid.UUID, entityName, table string, op MigrationOp, policy QuarantinePolicy, migrationID string) (MigrationOutcome, error) {
	report, err := Preflight(ctx, pool, tenantID, table, op)
	if err != nil {
		return MigrationOutcome{}, err
	}
	var outcome MigrationOutcome

	if report.BadRows > 0 {
		switch policy.Kind {
		case PolicyBlock:
			return outcome, fmt.Errorf("migration blocked: %d row(s) would fail this op (policy: Block)", report.BadRows)
		case PolicyQuarantine:
			if predicate, ok := badRowPredicate(op); ok {
				q, err := QuarantineBadRows(ctx, pool, tenantID, table, predicate, migrationID, "migration_preflight_failed")
				if err != nil {
					return outcome, err
				}
				outcome.Quarantined = q.Quarantined
				outcome.SkippedReferenced = q.SkippedReferenced
			}
		case PolicyCoerce:
			// No separate step — applySQL below builds the fallback directly into the
			// transform's SET clause, so a would-fail row is coerced in the same pass instead
			// of needing to be found and touched twice.
		}
	}

	// Ops that change the column itself (widen, drop) are one DDL statement, not a batched row
	// update: they run once, transactionally.
	if stmt, ok, derr := applyDDL(op, table, policy); derr != nil {
		return outcome, derr
	} else if ok {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			return outcome, err
		}
		return outcome, nil
	}

	opID := fmt.Sprintf("migration:%s:%s", table, migrationID)
	setClause, whereExtra, hasSQL, err := applySQL(op, table, policy)
	if err != nil {
		return outcome, err
	}
	if hasSQL {
		// ScopeSingleTenant: every current caller of RunMigration already reconciles one real
		// tenant at a time.
		if err := RunBatchedUpdate(ctx, pool, tenantID, entityName, table, opID, setClause, whereExtra, ScopeSingleTenant); err != nil {
			return outcome, err
		}
	}

	return outcome, nil
}

// applyDDL builds the single ALTER TABLE statement for the ops that change a column itself.
// ok is false for ops handled as batched row updates (applySQL) or that need nothing.
//
// Widening a type rewrites the table under an ACCESS EXCLUSIVE lock (Postgres cannot change a
// column's type online). That is the price of typed columns; rows that cannot convert were
// already handled by the quarantine policy before this runs.
func applyDDL(op MigrationOp, table string, policy QuarantinePolicy) (stmt string, ok bool, err error) {
	t := quoteQualifiedIdent(table)
	switch op.Kind {
	case MigWidenType:
		col := quoteIdent(op.WidenField)
		var using string
		if policy.Kind == PolicyCoerce {
			fallbackText, ferr := scalarFallbackText(policy.CoerceFallback)
			if ferr != nil {
				return "", false, ferr
			}
			using = fmt.Sprintf("CASE WHEN pg_input_is_valid(%s::text, %s) THEN %s::text::%s ELSE %s::%s END",
				col, quoteLiteral(op.WidenToSQLType), col, op.WidenToSQLType, quoteLiteral(fallbackText), op.WidenToSQLType)
		} else {
			using = fmt.Sprintf("%s::text::%s", col, op.WidenToSQLType)
		}
		return fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s TYPE %s USING %s", t, col, op.WidenToSQLType, using), true, nil
	case MigDropField:
		if op.DropKeepData {
			return "", false, nil
		}
		return fmt.Sprintf("ALTER TABLE %s DROP COLUMN IF EXISTS %s", t, quoteIdent(op.DropField)), true, nil
	default:
		return "", false, nil
	}
}

// applySQL builds the (SET clause, optional extra WHERE) for RunBatchedUpdate — hasSQL is false
// for an op with nothing to backfill. A rename is a RenameColumn DdlOp (Diff's renames), never a
// row update, so it needs nothing here.
func applySQL(op MigrationOp, table string, policy QuarantinePolicy) (setClause, whereExtra string, hasSQL bool, err error) {
	switch op.Kind {
	case MigRenameField, MigWidenType, MigDropField:
		return "", "", false, nil

	case MigAddField:
		if op.AddFieldDefault == nil {
			return "", "", false, nil
		}
		// jsonb_populate_record casts the JSON default to the column's own type, so one
		// expression serves every field kind.
		col := quoteIdent(op.AddFieldName)
		return fmt.Sprintf("%s = (jsonb_populate_record(NULL::%s, jsonb_build_object(%s, %s::jsonb))).%s",
				col, quoteQualifiedIdent(table), quoteLiteral(op.AddFieldName), quoteLiteral(string(op.AddFieldDefault)), col),
			fmt.Sprintf("t.%s IS NULL", col), true, nil

	case MigRemoveEnum:
		col := quoteIdent(op.RemoveEnumField)
		return fmt.Sprintf("%s = %s", col, quoteLiteral(op.RemoveEnumRemapTo)),
			fmt.Sprintf("t.%s = %s", col, quoteLiteral(op.RemoveEnumValue)), true, nil

	default:
		return "", "", false, fmt.Errorf("reconciler: unknown MigrationOpKind %d", op.Kind)
	}
}
