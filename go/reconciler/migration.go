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
	f := quoteLiteral(op.WidenField)
	ty := quoteLiteral(op.WidenToSQLType)
	return fmt.Sprintf("jsonb_typeof(t.data -> %s) = 'string' AND NOT pg_input_is_valid(t.data ->> %s, %s)", f, f, ty), true
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

	opID := fmt.Sprintf("migration:%s:%s", table, migrationID)
	setClause, whereExtra, hasSQL, err := applySQL(op, policy)
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

// applySQL builds the (SET clause, optional extra WHERE) for RunBatchedUpdate — hasSQL is false
// for an op with nothing to backfill (a DropField with DropKeepData only ever needs a metadata
// change, no data touched).
func applySQL(op MigrationOp, policy QuarantinePolicy) (setClause, whereExtra string, hasSQL bool, err error) {
	switch op.Kind {
	case MigRenameField:
		fromLit := quoteLiteral(op.RenameFrom)
		toLit := quoteLiteral(op.RenameTo)
		return fmt.Sprintf("data = (t.data - %s) || jsonb_build_object(%s, t.data -> %s)", fromLit, toLit, fromLit),
			fmt.Sprintf("t.data ? %s", fromLit), true, nil

	case MigAddField:
		if op.AddFieldDefault == nil {
			return "", "", false, nil
		}
		nameLit := quoteLiteral(op.AddFieldName)
		return fmt.Sprintf("data = jsonb_set(t.data, ARRAY[%s], %s::jsonb, true)", nameLit, quoteLiteral(string(op.AddFieldDefault))),
			fmt.Sprintf("NOT (t.data ? %s)", nameLit), true, nil

	case MigWidenType:
		f := quoteLiteral(op.WidenField)
		var castExpr string
		if policy.Kind == PolicyCoerce {
			fallbackText, ferr := scalarFallbackText(policy.CoerceFallback)
			if ferr != nil {
				return "", "", false, ferr
			}
			castExpr = fmt.Sprintf("CASE WHEN pg_input_is_valid(t.data ->> %s, %s) THEN (t.data ->> %s)::%s ELSE %s::%s END",
				f, quoteLiteral(op.WidenToSQLType), f, op.WidenToSQLType, quoteLiteral(fallbackText), op.WidenToSQLType)
		} else {
			castExpr = fmt.Sprintf("(t.data ->> %s)::%s", f, op.WidenToSQLType)
		}
		return fmt.Sprintf("data = jsonb_set(t.data, ARRAY[%s], to_jsonb((%s)))", f, castExpr),
			fmt.Sprintf("jsonb_typeof(t.data -> %s) = 'string'", f), true, nil

	case MigDropField:
		if op.DropKeepData {
			return "", "", false, nil
		}
		f := quoteLiteral(op.DropField)
		return fmt.Sprintf("data = t.data - %s", f), fmt.Sprintf("t.data ? %s", f), true, nil

	case MigRemoveEnum:
		f := quoteLiteral(op.RemoveEnumField)
		return fmt.Sprintf("data = jsonb_set(t.data, ARRAY[%s], to_jsonb(%s::text))", f, quoteLiteral(op.RemoveEnumRemapTo)),
			fmt.Sprintf("t.data ->> %s = %s", f, quoteLiteral(op.RemoveEnumValue)), true, nil

	default:
		return "", "", false, fmt.Errorf("reconciler: unknown MigrationOpKind %d", op.Kind)
	}
}
