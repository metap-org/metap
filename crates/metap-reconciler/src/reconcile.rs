//! `reconcile(desired) = introspect(actual) → diff → plan → execute` (§5.1) — the top-level
//! entry point wiring `compile`/`introspect`/`diff`/`executor` together for one
//! `(tenant, entity)`. Scoped to a single entity/tenant (`docs/features/04-table-per-entity.md`
//! step 2) — no orchestrator fan-out across tenants (step 4) and no cross-entity FK topo-sort
//! (a `Reference` field's FK is only ever emitted if `ref_entity` already has its own table —
//! see `compile::compile`'s doc comment; a caller driving many entities is responsible for
//! reconciling a referenced entity before the one that references it, exactly what the
//! orchestrator will automate later).

use metap_metadata::EntityDefinition;
use sqlx::PgPool;
use uuid::Uuid;

use crate::{compile, diff, executor, introspect};

#[derive(Debug, Clone)]
pub struct ReconcileOutcome {
    pub table: String,
    pub ops_applied: usize,
}

pub async fn reconcile(
    pool: &PgPool,
    tenant_id: Uuid,
    entity: &EntityDefinition,
    renames: &[(String, String)],
) -> anyhow::Result<ReconcileOutcome> {
    let desired = compile::compile(entity)?;
    let actual = introspect::introspect(pool, tenant_id, &entity.name, &desired.table).await?;
    let ops = diff::diff(&desired, actual.as_ref(), renames);
    let ops_applied = ops.len();

    executor::execute(pool, tenant_id, &entity.name, &desired, &ops).await?;

    Ok(ReconcileOutcome {
        table: desired.table,
        ops_applied,
    })
}
