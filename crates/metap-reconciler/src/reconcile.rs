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

use crate::backfill::BackfillScope;
use crate::{compile, diff, executor, introspect};

#[derive(Debug, Clone)]
pub struct ReconcileOutcome {
    pub table: String,
    pub ops_applied: usize,
}

/// Historical signature, unchanged — every existing caller (including 2 sibling repos out of
/// this session's repo access to update in step) gets [`BackfillScope::SingleTenant`], correct
/// for the common case of reconciling one real tenant's own table. A caller reconciling a
/// `Schema`-strategy shared table at boot with `metap_control::PLATFORM_TENANT_ID`'s sentinel
/// (`metap-app::MetapApp::with_entities`, and any downstream binary doing the equivalent by hand
/// — see [`BackfillScope`]'s own doc comment for the failure this distinction exists to prevent)
/// needs [`reconcile_with_scope`] instead.
pub async fn reconcile(
    pool: &PgPool,
    tenant_id: Uuid,
    entity: &EntityDefinition,
    renames: &[(String, String)],
) -> anyhow::Result<ReconcileOutcome> {
    reconcile_with_scope(pool, tenant_id, entity, renames, BackfillScope::SingleTenant).await
}

pub async fn reconcile_with_scope(
    pool: &PgPool,
    tenant_id: Uuid,
    entity: &EntityDefinition,
    renames: &[(String, String)],
    scope: BackfillScope,
) -> anyhow::Result<ReconcileOutcome> {
    let desired = compile::compile(entity)?;
    let actual = introspect::introspect(pool, tenant_id, &entity.name, &desired.table).await?;
    let ops = diff::diff(&desired, actual.as_ref(), renames);
    let ops_applied = ops.len();

    executor::execute_with_scope(pool, tenant_id, &entity.name, &desired, &ops, scope).await?;

    Ok(ReconcileOutcome {
        table: desired.table,
        ops_applied,
    })
}
