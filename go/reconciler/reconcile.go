// Reconcile(desired) = Introspect(actual) -> Diff -> plan -> Execute — the top-level entry point
// wiring Compile/Introspect/Diff/Execute together for one (tenant, entity). Scoped to a single
// entity/tenant — no orchestrator fan-out across tenants and no cross-entity FK topo-sort (a
// caller driving many entities is responsible for reconciling a referenced entity before the one
// that references it).
package reconciler

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/metap-org/metap-go/metadata"
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
	desired, err := Compile(entity)
	if err != nil {
		return ReconcileOutcome{}, err
	}
	actual, err := Introspect(ctx, pool, tenantID, entity.Name, desired.Table)
	if err != nil {
		return ReconcileOutcome{}, err
	}
	ops := Diff(desired, actual, renames)
	opsApplied := len(ops)

	if err := ExecuteWithScope(ctx, pool, tenantID, entity.Name, desired, ops, scope); err != nil {
		return ReconcileOutcome{}, err
	}

	return ReconcileOutcome{Table: desired.Table, OpsApplied: opsApplied}, nil
}
