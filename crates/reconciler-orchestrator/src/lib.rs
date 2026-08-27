//! The ticker half of `metap-reconciler::orchestrator` (`docs/multi-tenant-platform-design.md`
//! §6, `docs/features/04-table-per-entity.md` step 6) — that module builds the pull-based
//! claim/failure-classification/wave-rollout *primitives* but deliberately never runs as a
//! service (see its own doc comment: "Like `metap-cron` (a library) vs. `cron-scheduler` (the
//! binary that ticks it on a timer)"). This crate is that binary: a loop that claims due
//! `(tenant, entity)` work from `reconciler_entity_deployments` and actually calls
//! `metap_reconciler::reconcile()` for each one, same shape `cron-scheduler`'s `ticker` module
//! already established (biased `tokio::select!` against shutdown, then a sleep, both
//! shutdown-interruptible).
//!
//! **Which `EntityDefinition` a claimed `(tenant, entity)` reconciles against**: this binary
//! must stay entity-agnostic like every other ops binary in this repo (no `metap-*` crate gets
//! business-entity knowledge) — a code-authored entity (`crm.customers`, `jira.issues`, ...)
//! only exists inside the specific app binary that registers it, so a generic ops process has
//! no way to resolve one. DB-authored (low-code) entity definitions are different: Phase A
//! decided they're global metadata stored in Postgres (`metap_lowcode::get_published`), which
//! *any* process holding the right pool can read — the only entity source this crate can
//! reach without linking business code. So today this loop drives table-per-entity for
//! **published low-code entities only**; a code-authored entity keeps using its host binary's
//! own direct boot-time `reconcile()` call (`apps/crm-server`/`apps/jira-server`'s `main.rs`)
//! exactly as it does today — this crate doesn't replace that, it covers the case those can't
//! (many tenants, one shared low-code entity pack).
//!
//! **Which pool `reconciler_entity_deployments` itself lives in**: `crates/migrations/*.sql`
//! (including `0018_reconciler_orchestrator.sql`) applies to every tenant database — a
//! `DedicatedDb` tenant's own database gets its own private copy of this table, so it can never
//! see another tenant's rows. Real cross-tenant fan-out (the wave-rollout scenario §6.4
//! describes) only happens for `Schema`-strategy tenants sharing the platform's own pool, which
//! is what `control_pool` below always is — a `DedicatedDb` tenant reconciling itself would need
//! a separate poll of its own database, not built here (same category of gap
//! `outbox-publisher`'s bullet in `CLAUDE.md` describes for that binary, and not yet a real
//! need: no `DedicatedDb` tenant runs a low-code entity through the orchestrator today).

use std::time::Duration;

use metap_control::Router;
use metap_reconciler::orchestrator::{claim_due, run_claimed_batch, ClaimedEntity};
use sqlx::PgPool;

#[derive(Debug, Clone)]
pub struct OrchestratorConfig {
    pub worker_id: String,
    pub poll_interval: Duration,
    pub batch_limit: i64,
    pub max_attempts: i32,
    pub concurrency_limit: usize,
    /// `None` (production default) claims across every entity — one global work queue, matching
    /// `claim_due`'s own doc comment. `Some(name)` scopes a worker to one entity — a legitimate
    /// production shard-by-entity shape, and also how this crate's own e2e tests (run
    /// concurrently by the Rust test harness, all sharing one `reconciler_entity_deployments`
    /// table) avoid claiming each other's rows.
    pub entity_name_filter: Option<String>,
}

/// One claim-and-reconcile cycle — pulled out of the loop so tests can call it directly
/// (deterministic: one call, one batch, no sleeping) instead of racing a background loop.
pub async fn run_once(control_pool: &PgPool, router: &Router, config: &OrchestratorConfig) -> anyhow::Result<usize> {
    let claimed = claim_due(
        control_pool,
        &config.worker_id,
        config.entity_name_filter.as_deref(),
        config.max_attempts,
        config.batch_limit,
    )
    .await?;
    if claimed.is_empty() {
        return Ok(0);
    }
    let claimed_count = claimed.len();
    tracing::info!(claimed = claimed_count, "orchestrator claimed due entity deployments");

    let outcomes = run_claimed_batch(control_pool, claimed, config.concurrency_limit, |entity| {
        let router = router.clone();
        async move { reconcile_one(&router, entity).await }
    })
    .await;

    for outcome in &outcomes {
        match &outcome.result {
            Ok(()) => tracing::info!(
                tenant_id = %outcome.entity.tenant_id,
                entity = outcome.entity.entity_name,
                version = outcome.entity.desired_version,
                "reconciled"
            ),
            Err(err) => tracing::warn!(
                tenant_id = %outcome.entity.tenant_id,
                entity = outcome.entity.entity_name,
                version = outcome.entity.desired_version,
                error = %err,
                "reconcile failed"
            ),
        }
    }

    Ok(claimed_count)
}

/// Resolves the claimed entity's published low-code definition and reconciles it into a
/// dedicated table (`metap_reconciler::qualified_table_name_for`) — table-per-entity is what
/// this queue exists to drive, so a claimed entity is always opted into it here regardless of
/// what `LowCodeEntityDefinition::to_entity_definition`'s own default (`"records"`) would give
/// it standalone. `renames: &[]` — this loop doesn't thread through the migration/rename ops
/// `metap_reconciler::migration` supports; a claimed entity mid-rename would need those passed
/// explicitly (out of scope here, same as every other direct `reconcile()` call site in this
/// repo today — `apps/crm-server`/`apps/jira-server`'s boot sequences don't pass renames either).
async fn reconcile_one(router: &Router, entity: ClaimedEntity) -> anyhow::Result<()> {
    let pool = router.pool_for(entity.tenant_id.into()).await?;
    let published = metap_lowcode::get_published(&pool, &entity.entity_name)
        .await?
        .ok_or_else(|| anyhow::anyhow!("no published low-code definition for entity \"{}\"", entity.entity_name))?;

    let mut def = published.definition.to_entity_definition();
    def.table_name = metap_reconciler::qualified_table_name_for(&entity.entity_name);

    metap_reconciler::reconcile(&pool, entity.tenant_id, &def, &[]).await?;
    Ok(())
}

/// The always-on loop `src/main.rs` runs — mirrors `cron_scheduler::ticker::run_ticker`'s shape
/// exactly (biased select against shutdown before *and* after each tick, so a shutdown signal
/// interrupts either the work or the sleep, never blocks on either).
pub async fn run(
    control_pool: PgPool,
    router: Router,
    config: OrchestratorConfig,
    shutdown: impl std::future::Future<Output = ()>,
) -> anyhow::Result<()> {
    let mut shutdown = std::pin::pin!(shutdown);

    loop {
        tokio::select! {
            biased;
            _ = &mut shutdown => {
                tracing::info!("shutdown signal received, exiting reconciler-orchestrator");
                return Ok(());
            }
            result = run_once(&control_pool, &router, &config) => {
                if let Err(err) = result {
                    tracing::error!(error = %err, "orchestrator tick failed");
                }
            }
        }

        tokio::select! {
            biased;
            _ = &mut shutdown => {
                tracing::info!("shutdown signal received, exiting reconciler-orchestrator");
                return Ok(());
            }
            _ = tokio::time::sleep(config.poll_interval) => {}
        }
    }
}
