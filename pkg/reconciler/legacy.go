// One-time move of a pre-Phase-106 table (fields stored in a data jsonb column) to native
// columns. Three explicit steps, never run implicitly by a normal Reconcile:
//
//  1. MigrateLegacy   — add every field column, copy data -> column in resumable batches, then
//     drop the old sync triggers. data is left intact, so the old code keeps working until cutover.
//  2. VerifyLegacy    — count, per field, rows whose data holds a value the column does not.
//  3. DropLegacyData  — only when step 2 reports nothing missing: drop the data column.
//
// docs/roadmap/106-native-entity-columns-no-data-jsonb.md section 3.
package reconciler

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/metap-org/metap/pkg/metadata"
)

// MigrateLegacy runs steps 1 of the move. Resumable: a crash leaves the backfill ledger at the
// last completed batch and a second call continues from there.
func MigrateLegacy(ctx context.Context, pool *pgxpool.Pool, tenantID uuid.UUID, entity metadata.EntityDefinition, scope BackfillScope) (ReconcileOutcome, error) {
	return ReconcileWithOptions(ctx, pool, tenantID, entity, nil, ReconcileOptions{Scope: scope, MigrateLegacy: true})
}

// LegacyReport is VerifyLegacy's result.
type LegacyReport struct {
	Rows int64
	// MissingByField: field -> rows where data has a non-null value for it but the column is NULL.
	MissingByField map[string]int64
	// StillHasLegacyData: whether the data column exists (false once DropLegacyData ran).
	StillHasLegacyData bool
	// SyncTriggersLeft: old sync triggers MigrateLegacy has not dropped yet.
	SyncTriggersLeft []string
}

// Ok: nothing blocks DropLegacyData.
func (r LegacyReport) Ok() bool {
	if !r.StillHasLegacyData {
		return true
	}
	if len(r.SyncTriggersLeft) > 0 {
		return false
	}
	for _, n := range r.MissingByField {
		if n > 0 {
			return false
		}
	}
	return true
}

func VerifyLegacy(ctx context.Context, pool *pgxpool.Pool, tenantID uuid.UUID, entity metadata.EntityDefinition) (LegacyReport, error) {
	desired, err := Compile(entity)
	if err != nil {
		return LegacyReport{}, err
	}
	actual, err := Introspect(ctx, pool, tenantID, entity.Name, desired.Table)
	if err != nil {
		return LegacyReport{}, err
	}
	report := LegacyReport{MissingByField: map[string]int64{}}
	if actual == nil {
		return report, nil
	}
	report.StillHasLegacyData = actual.HasLegacyData
	report.SyncTriggersLeft = actual.LegacySyncTriggers
	t := quoteQualifiedIdent(desired.Table)
	if err := pool.QueryRow(ctx, fmt.Sprintf("SELECT count(*) FROM %s", t)).Scan(&report.Rows); err != nil {
		return LegacyReport{}, err
	}
	if !actual.HasLegacyData {
		return report, nil
	}
	for name, spec := range desired.Columns {
		if spec.Origin.Kind != OriginNative {
			continue
		}
		if _, ok := actual.Columns[name]; !ok {
			// Column not even added yet: every row holding a value for it is missing.
			var n int64
			if err := pool.QueryRow(ctx, fmt.Sprintf(
				"SELECT count(*) FROM %s t WHERE t.data ? %s AND jsonb_typeof(t.data -> %s) <> 'null'", t, quoteLiteral(name), quoteLiteral(name))).Scan(&n); err != nil {
				return LegacyReport{}, err
			}
			report.MissingByField[name] = n
			continue
		}
		var n int64
		q := fmt.Sprintf(
			"SELECT count(*) FROM %s t WHERE t.data ? %s AND jsonb_typeof(t.data -> %s) <> 'null' AND t.%s IS NULL",
			t, quoteLiteral(name), quoteLiteral(name), quoteIdent(name))
		if err := pool.QueryRow(ctx, q).Scan(&n); err != nil {
			return LegacyReport{}, err
		}
		report.MissingByField[name] = n
	}
	return report, nil
}

// DropLegacyData is the irreversible step: it refuses unless VerifyLegacy is clean, so the data
// column can never be dropped while a value exists only there.
func DropLegacyData(ctx context.Context, pool *pgxpool.Pool, tenantID uuid.UUID, entity metadata.EntityDefinition) error {
	report, err := VerifyLegacy(ctx, pool, tenantID, entity)
	if err != nil {
		return err
	}
	if !report.StillHasLegacyData {
		return nil
	}
	if !report.Ok() {
		return fmt.Errorf("%s: refusing to drop the data column: missing %v, sync triggers left %v", entity.Name, report.MissingByField, report.SyncTriggersLeft)
	}
	desired, err := Compile(entity)
	if err != nil {
		return err
	}
	_, err = pool.Exec(ctx, fmt.Sprintf("ALTER TABLE %s DROP COLUMN data", quoteQualifiedIdent(desired.Table)))
	return err
}

// RestoreLegacyData is the way back: it rebuilds the pre-Phase-106 shape so the previous release of the
// application can run again. It works both before DropLegacyData (data still there, probably stale for
// rows the new code wrote) and after it (data column is added back).
//
//  1. add the data column if it is missing (a constant default, so no table rewrite);
//  2. rewrite data from the columns for every row, in resumable batches (a full copy, so rows written
//     by the new code are included);
//  3. recreate the old sync trigger for every field the old code kept a real column for (a Reference
//     with a target, or Storage: column), since that code reads the column and expects data to feed it.
//
// The native columns are left in place; the old code ignores columns it does not know. Indexes the old
// code builds on data are recreated by its own reconcile at boot.
func RestoreLegacyData(ctx context.Context, pool *pgxpool.Pool, tenantID uuid.UUID, entity metadata.EntityDefinition, scope BackfillScope) (LegacyReport, error) {
	desired, err := Compile(entity)
	if err != nil {
		return LegacyReport{}, err
	}
	actual, err := Introspect(ctx, pool, tenantID, entity.Name, desired.Table)
	if err != nil {
		return LegacyReport{}, err
	}
	if actual == nil {
		return LegacyReport{}, fmt.Errorf("%s: table does not exist, nothing to restore", desired.Table)
	}
	t := quoteQualifiedIdent(desired.Table)
	if !actual.HasLegacyData {
		if _, err := pool.Exec(ctx, fmt.Sprintf("ALTER TABLE %s ADD COLUMN data jsonb NOT NULL DEFAULT '{}'::jsonb", t)); err != nil {
			return LegacyReport{}, err
		}
	}

	opID := "restore_legacy_data:" + desired.Table
	// Forget this restore's own progress (a re-run copies everything again) and the forward migration's
	// per-column "done" marks: the old code is about to write data only, so a later MigrateLegacy must
	// copy data -> columns afresh instead of trusting that the columns are already filled.
	if _, err := pool.Exec(ctx, `DELETE FROM metadata.reconciler_backfill_progress
		WHERE tenant_id = $1 AND entity_name = $2 AND (op_id = $3 OR op_id LIKE $4)`,
		tenantID, entity.Name, opID, "backfill:"+desired.Table+":%"); err != nil {
		return LegacyReport{}, err
	}
	if err := RunBatchedUpdate(ctx, pool, tenantID, entity.Name, desired.Table, opID, "data = "+columnsAsJSON(entity), "", scope); err != nil {
		return LegacyReport{}, err
	}

	for _, f := range entity.Fields {
		if frameworkFieldNeedsNoTrigger(f.Name) || !hadRealColumnBefore(f) {
			continue
		}
		for _, stmt := range buildSyncTriggerSQL(desired.Table, f.Name, metadata.FieldKindSQLType(f.Kind)) {
			if _, err := pool.Exec(ctx, stmt); err != nil {
				return LegacyReport{}, err
			}
		}
	}
	return verifyRestored(ctx, pool, desired, entity)
}

func frameworkFieldNeedsNoTrigger(name string) bool { return name == "code" || name == "status" }

// hadRealColumnBefore: which fields the pre-Phase-106 code gave a real, trigger-synced column.
func hadRealColumnBefore(f metadata.EntityField) bool {
	return (f.Kind == metadata.KindReference && f.RefEntity != nil) || (f.Storage != nil && *f.Storage == metadata.StorageColumn)
}

// columnsAsJSON is the SQL expression, over the row aliased t, that builds the old data object from
// the native columns: every non-NULL column becomes a key (a JSON field keeps its contents).
func columnsAsJSON(entity metadata.EntityDefinition) string {
	if len(entity.Fields) == 0 {
		return "'{}'::jsonb"
	}
	pairs := make([]string, 0, len(entity.Fields))
	for _, f := range entity.Fields {
		col := "t." + quoteIdent(f.Name)
		if frameworkFieldNeedsNoTrigger(f.Name) {
			col = "t." + f.Name
		}
		pairs = append(pairs, fmt.Sprintf("(%s, to_jsonb(%s))", quoteLiteral(f.Name), col))
	}
	return "(SELECT COALESCE(jsonb_object_agg(x.k, x.v), '{}'::jsonb) FROM (VALUES " + strings.Join(pairs, ", ") + ") AS x(k, v) WHERE x.v IS NOT NULL)"
}

// verifyRestored counts, per field, rows where the column has a value that data lacks.
func verifyRestored(ctx context.Context, pool *pgxpool.Pool, desired PhysicalSchema, entity metadata.EntityDefinition) (LegacyReport, error) {
	t := quoteQualifiedIdent(desired.Table)
	report := LegacyReport{MissingByField: map[string]int64{}, StillHasLegacyData: true}
	if err := pool.QueryRow(ctx, fmt.Sprintf("SELECT count(*) FROM %s", t)).Scan(&report.Rows); err != nil {
		return LegacyReport{}, err
	}
	for _, f := range entity.Fields {
		col := "t." + quoteIdent(f.Name)
		if frameworkFieldNeedsNoTrigger(f.Name) {
			col = "t." + f.Name
		}
		var n int64
		q := fmt.Sprintf("SELECT count(*) FROM %s t WHERE %s IS NOT NULL AND NOT (t.data ? %s)", t, col, quoteLiteral(f.Name))
		if err := pool.QueryRow(ctx, q).Scan(&n); err != nil {
			return LegacyReport{}, err
		}
		report.MissingByField[f.Name] = n
	}
	return report, nil
}
