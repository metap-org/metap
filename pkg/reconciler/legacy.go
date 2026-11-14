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
