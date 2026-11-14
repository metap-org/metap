// Reconcile(desired) = Introspect(actual) -> Diff -> plan -> Execute — the top-level entry point
// wiring Compile/Introspect/Diff/Execute together for one (tenant, entity). Scoped to a single
// entity/tenant — no orchestrator fan-out across tenants and no cross-entity FK topo-sort (a
// caller driving many entities is responsible for reconciling a referenced entity before the one
// that references it).
package reconciler

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/metap-org/metap/pkg/metadata"
)

type ReconcileOutcome struct {
	Table      string
	OpsApplied int
}

// Reconcile is the historical signature, unchanged — every existing caller gets
// ScopeSingleTenant, correct for the common case of reconciling one real tenant's own table. A
// caller reconciling a Schema-strategy shared table at boot with a sentinel tenant id needs
// ReconcileWithScope instead.
func Reconcile(ctx context.Context, pool *pgxpool.Pool, tenantID uuid.UUID, entity metadata.EntityDefinition, renames [][2]string) (ReconcileOutcome, error) {
	return ReconcileWithScope(ctx, pool, tenantID, entity, renames, ScopeSingleTenant)
}

func ReconcileWithScope(ctx context.Context, pool *pgxpool.Pool, tenantID uuid.UUID, entity metadata.EntityDefinition, renames [][2]string, scope BackfillScope) (ReconcileOutcome, error) {
	return ReconcileWithOptions(ctx, pool, tenantID, entity, renames, ReconcileOptions{Scope: scope})
}

// ReconcileOptions: Scope as BackfillScope's own doc comment describes. MigrateLegacy turns on
// the one-time move of a pre-Phase-106 table (still carrying its data jsonb column) to native
// columns — see MigrateLegacy/DropLegacyData in legacy.go. Left false, reconciling such a table
// is refused (ErrLegacyDataColumn) rather than silently serving NULL columns that have not been
// filled yet.
type ReconcileOptions struct {
	Scope         BackfillScope
	MigrateLegacy bool
}

// ErrLegacyDataColumn: the table still has the pre-Phase-106 data jsonb column, so its rows have
// not been moved to native columns and CrudService (which reads columns only) would see NULLs.
var ErrLegacyDataColumn = errors.New("table still has the legacy data column; migrate it to native columns first (dev-tools migrate-to-native-columns)")

func ReconcileWithOptions(ctx context.Context, pool *pgxpool.Pool, tenantID uuid.UUID, entity metadata.EntityDefinition, renames [][2]string, opts ReconcileOptions) (ReconcileOutcome, error) {
	desired, err := Compile(entity)
	if err != nil {
		return ReconcileOutcome{}, err
	}
	actual, err := Introspect(ctx, pool, tenantID, entity.Name, desired.Table)
	if err != nil {
		return ReconcileOutcome{}, err
	}
	if actual != nil && actual.HasLegacyData && !opts.MigrateLegacy {
		return ReconcileOutcome{}, fmt.Errorf("%s: %w", desired.Table, ErrLegacyDataColumn)
	}
	ops := Diff(desired, actual, renames)
	opsApplied := len(ops)

	if err := ExecuteWithScope(ctx, pool, tenantID, entity.Name, desired, ops, opts.Scope); err != nil {
		return ReconcileOutcome{}, err
	}

	return ReconcileOutcome{Table: desired.Table, OpsApplied: opsApplied}, nil
}
