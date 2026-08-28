//! §6 — fan-out multi-tenant. `reconciler_entity_deployments`
//! (`crates/migrations/0018_reconciler_orchestrator.sql`) tracks version skew:
//! `desired_version > applied_version` is both the work queue and the global resume source
//! (§6.1) — an orchestrator process dying mid-rollout loses nothing, a restart just re-queries
//! this table. Pull-based claim with `FOR UPDATE SKIP LOCKED` (§6.2) so many workers never grab
//! the same `(tenant, entity)`. Concurrency bounded by a caller-sized limit (§6.3 — "trial"
//! tenants sharing one DB want this low, "paid" dedicated-DB tenants can go higher; this crate
//! doesn't know which, that's `metap-control`'s tenant-tier concept, a layer up). Failure
//! classified from SQLSTATE (§6.4) so only genuinely transient errors retry automatically. Wave
//! rollout (§6.4) so a bad pack is caught on 1-2 canary tenants, not the whole fleet.
//!
//! What this module does **not** do: run forever as a service. Like `metap-cron` (a library)
//! vs. `cron-scheduler` (the binary that ticks it on a timer), turning this into a long-running
//! process is a separate deliverable this crate doesn't include.

use std::collections::{HashMap, HashSet};

use futures::stream::{self, StreamExt};
use metap_metadata::{EntityDefinition, FieldKind};
use sqlx::PgPool;
use uuid::Uuid;

#[derive(Debug, Clone)]
pub struct ClaimedEntity {
    pub tenant_id: Uuid,
    pub entity_name: String,
    pub desired_version: i64,
}

/// Orders a claimed batch into dependency waves so a `Reference` field's target entity is
/// reconciled before the entity that references it, when both are claimed together — without
/// this, `run_claimed_batch`'s `buffer_unordered` can race the two, and a first-time FK
/// constraint can be attempted against a target table that doesn't exist yet purely because of
/// scheduling, not a real data problem. Only intra-tenant edges matter (FK targets live in the
/// claiming tenant's own database) — entities from different tenants never gate each other, and
/// a dependency on an entity outside this batch (already reconciled in an earlier tick) needs no
/// ordering here since its table already exists. A dependency cycle can't be linearized: the
/// remaining cyclic entities are placed together in one final wave, best-effort (same outcome as
/// today's unordered batch for that subset — this only removes the race for the common acyclic
/// case).
pub fn topo_sort_waves(entities: Vec<(ClaimedEntity, EntityDefinition)>) -> Vec<Vec<ClaimedEntity>> {
    let mut by_tenant: HashMap<Uuid, Vec<(ClaimedEntity, EntityDefinition)>> = HashMap::new();
    for pair in entities {
        by_tenant.entry(pair.0.tenant_id).or_default().push(pair);
    }

    let per_tenant_waves: Vec<Vec<Vec<ClaimedEntity>>> = by_tenant.into_values().map(tenant_waves).collect();

    let wave_count = per_tenant_waves.iter().map(Vec::len).max().unwrap_or(0);
    let mut merged = vec![Vec::new(); wave_count];
    for waves in per_tenant_waves {
        for (i, wave) in waves.into_iter().enumerate() {
            merged[i].extend(wave);
        }
    }
    merged
}

/// Kahn's algorithm over one tenant's claimed entities, edges from `Reference` fields pointing at
/// another entity claimed in the same batch.
fn tenant_waves(items: Vec<(ClaimedEntity, EntityDefinition)>) -> Vec<Vec<ClaimedEntity>> {
    let names: HashSet<&str> = items.iter().map(|(c, _)| c.entity_name.as_str()).collect();
    let mut claimed_by_name: HashMap<String, ClaimedEntity> = HashMap::new();
    let mut deps: HashMap<String, HashSet<String>> = HashMap::new();
    for (claimed, def) in &items {
        let entity_deps = def
            .fields
            .iter()
            .filter(|f| f.kind == FieldKind::Reference)
            .filter_map(|f| f.ref_entity.as_deref())
            .filter(|&r| r != claimed.entity_name && names.contains(r))
            .map(str::to_string)
            .collect();
        deps.insert(claimed.entity_name.clone(), entity_deps);
        claimed_by_name.insert(claimed.entity_name.clone(), claimed.clone());
    }

    let mut remaining: HashSet<String> = claimed_by_name.keys().cloned().collect();
    let mut waves = Vec::new();
    while !remaining.is_empty() {
        let ready: Vec<String> = remaining
            .iter()
            .filter(|name| deps[*name].iter().all(|dep| !remaining.contains(dep)))
            .cloned()
            .collect();
        if ready.is_empty() {
            // Cycle among everything left — no valid order, run them together as a last wave.
            waves.push(remaining.iter().map(|n| claimed_by_name[n].clone()).collect());
            break;
        }
        waves.push(ready.iter().map(|n| claimed_by_name[n].clone()).collect());
        for name in &ready {
            remaining.remove(name);
        }
    }
    waves
}

/// §6.2 — `FOR UPDATE SKIP LOCKED`: many workers can call this concurrently against the same
/// table and never claim the same row twice, without a separate distributed lock. Fresh
/// (`pending`) work is tried before retries (`status = 'failed'` sorts last in the `ORDER BY`,
/// matching §6.2's exact query), and nothing past `max_attempts` is claimable at all.
/// `entity_name_filter: None` claims across every entity (matching §6.2's SQL verbatim — one
/// global work queue). `Some(name)` scopes the claim to just that entity — a legitimate
/// production shape too (sharding orchestrator workers by entity, not just a test-isolation
/// convenience), not just how the tests in `tests/orchestrator_postgres.rs` avoid stealing each
/// other's rows when Rust runs test functions concurrently in one process.
/// A `'running'` row whose `lease_heartbeat` is older than this is reclaimed by `claim_due` as
/// abandoned — found live (`AUDIT_2.md`): `lease_heartbeat` was written at claim time and never
/// read back anywhere, so a worker that crashed mid-`reconcile_one` (after `claim_due`, before
/// `record_success`/`record_failure`) left that `(tenant, entity)` permanently `'running'`,
/// excluded from `claim_due`'s own `status IN ('pending', 'failed')` filter forever. No caller in
/// this codebase currently refreshes `lease_heartbeat` *during* a long-running reconcile (nothing
/// wires this orchestrator into a running service yet — see this module's own doc comment), so
/// this window has to be generous enough to outlast a legitimately slow reconcile (a large
/// backfill can run for a while) rather than tuned tight — a real always-on deployment of this
/// orchestrator would want a periodic heartbeat refresh from inside `reconcile_one` so this could
/// shrink; that's a separate future improvement, not needed while nothing runs this loop
/// continuously.
const LEASE_STALE_AFTER_MINUTES: i64 = 60;

pub async fn claim_due(
    pool: &PgPool,
    worker_id: &str,
    entity_name_filter: Option<&str>,
    max_attempts: i32,
    limit: i64,
) -> anyhow::Result<Vec<ClaimedEntity>> {
    let stale_before = chrono::Utc::now() - chrono::Duration::minutes(LEASE_STALE_AFTER_MINUTES);
    let rows: Vec<(Uuid, String, i64)> = sqlx::query_as(
        "UPDATE reconciler_entity_deployments SET status = 'running', lease_worker = $1, \
         lease_heartbeat = now(), attempts = attempts + 1, updated_at = now() \
         WHERE (tenant_id, entity_name) IN ( \
           SELECT tenant_id, entity_name FROM reconciler_entity_deployments \
           WHERE desired_version > COALESCE(applied_version, 0) \
                 AND (status IN ('pending', 'failed') \
                      OR (status = 'running' AND lease_heartbeat < $5)) \
                 AND attempts < $2 AND ($4::text IS NULL OR entity_name = $4) \
           ORDER BY (status = 'failed') ASC, priority_tier ASC, updated_at ASC \
           LIMIT $3 FOR UPDATE SKIP LOCKED \
         ) RETURNING tenant_id, entity_name, desired_version",
    )
    .bind(worker_id)
    .bind(max_attempts)
    .bind(limit)
    .bind(entity_name_filter)
    .bind(stale_before)
    .fetch_all(pool)
    .await?;
    Ok(rows
        .into_iter()
        .map(|(tenant_id, entity_name, desired_version)| ClaimedEntity {
            tenant_id,
            entity_name,
            desired_version,
        })
        .collect())
}

/// Seeds or bumps one `(tenant, entity)`'s desired version — the missing "who populates the
/// work queue" half of §6.1 (`advance_wave` is the fleet-wide/canary version of this same
/// UPSERT; this is the single-tenant shape a CLI trigger or an admin action needs, without
/// wave/cohort bookkeeping). No-op if `desired_version` isn't actually newer — same
/// `WHERE ... < EXCLUDED.desired_version` guard `advance_wave` uses, so calling this twice with
/// the same version is harmless. Resets `attempts`/`failure_class`/`last_error` back to a clean
/// `pending` state so a previously `blocked` row can be retried by bumping the version (the
/// intended way to move past a `DataError`/`Fatal` block: fix the metadata/data, republish a
/// new version, re-enqueue).
pub async fn enqueue_deployment(
    pool: &PgPool,
    tenant_id: Uuid,
    entity_name: &str,
    desired_version: i64,
) -> anyhow::Result<()> {
    sqlx::query(
        "INSERT INTO reconciler_entity_deployments (tenant_id, entity_name, desired_version) \
         VALUES ($1, $2, $3) \
         ON CONFLICT (tenant_id, entity_name) DO UPDATE \
         SET desired_version = EXCLUDED.desired_version, status = 'pending', attempts = 0, \
             failure_class = NULL, last_error = NULL, updated_at = now() \
         WHERE reconciler_entity_deployments.desired_version < EXCLUDED.desired_version",
    )
    .bind(tenant_id)
    .bind(entity_name)
    .bind(desired_version)
    .execute(pool)
    .await?;
    Ok(())
}

/// §6.4's three SQLSTATE buckets: retry automatically, or don't (data needs a human to fix, or
/// the metadata/code itself is wrong).
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum FailureClass {
    Transient,
    DataError,
    Fatal,
}

impl FailureClass {
    fn as_str(self) -> &'static str {
        match self {
            FailureClass::Transient => "transient",
            FailureClass::DataError => "data_error",
            FailureClass::Fatal => "fatal",
        }
    }
}

/// Walks the error chain for a `sqlx::Error` carrying a Postgres SQLSTATE — `reconcile()`
/// wraps DB errors in `anyhow::Error` at several layers (`executor`, `backfill`, ...), so a
/// plain `downcast_ref` on the top-level error alone would miss it.
pub fn classify_error(err: &anyhow::Error) -> FailureClass {
    let sqlstate = err
        .chain()
        .find_map(|cause| cause.downcast_ref::<sqlx::Error>())
        .and_then(|e| e.as_database_error())
        .and_then(|d| d.code())
        .map(|c| c.into_owned());
    match sqlstate.as_deref() {
        // serialization_failure, deadlock_detected, lock_not_available, too_many_connections
        Some("40001" | "40P01" | "55P03" | "53300") => FailureClass::Transient,
        // foreign_key_violation, unique_violation, check_violation, invalid_text_representation,
        // numeric_value_out_of_range
        Some("23503" | "23505" | "23514" | "22P02" | "22003") => FailureClass::DataError,
        _ => FailureClass::Fatal,
    }
}

pub async fn record_success(
    pool: &PgPool,
    tenant_id: Uuid,
    entity_name: &str,
    applied_version: i64,
) -> anyhow::Result<()> {
    sqlx::query(
        "UPDATE reconciler_entity_deployments SET status = 'done', applied_version = $3, \
         failure_class = NULL, last_error = NULL, lease_worker = NULL, updated_at = now() \
         WHERE tenant_id = $1 AND entity_name = $2",
    )
    .bind(tenant_id)
    .bind(entity_name)
    .bind(applied_version)
    .execute(pool)
    .await?;
    Ok(())
}

/// `Transient` goes back to `'failed'` (still claimable, up to `max_attempts` — §6.2's query).
/// `DataError`/`Fatal` go to `'blocked'` — deliberately outside `claim_due`'s `status IN
/// ('pending', 'failed')` filter, so a human has to move it forward, not an automatic retry
/// loop hammering the same broken data or metadata.
pub async fn record_failure(
    pool: &PgPool,
    tenant_id: Uuid,
    entity_name: &str,
    err: &anyhow::Error,
) -> anyhow::Result<FailureClass> {
    let class = classify_error(err);
    let status = if class == FailureClass::Transient {
        "failed"
    } else {
        "blocked"
    };
    sqlx::query(
        "UPDATE reconciler_entity_deployments SET status = $3, failure_class = $4, last_error = $5, \
         lease_worker = NULL, updated_at = now() WHERE tenant_id = $1 AND entity_name = $2",
    )
    .bind(tenant_id)
    .bind(entity_name)
    .bind(status)
    .bind(class.as_str())
    .bind(format!("{err:#}"))
    .execute(pool)
    .await?;
    Ok(class)
}

#[derive(Debug)]
pub struct BatchOutcome {
    pub entity: ClaimedEntity,
    pub result: anyhow::Result<()>,
}

/// §6.3/§6.4 — runs `reconcile_one` for every claimed entity, at most `concurrency_limit` at a
/// time, and in isolation: one entity's error is recorded (`record_failure`) and does not stop
/// or cancel any other entity's work (§6.4: "một tenant fail KHÔNG chặn tenant khác"). Success
/// records `record_success` with the entity's own `desired_version` as the newly-applied one.
pub async fn run_claimed_batch<F, Fut>(
    pool: &PgPool,
    claimed: Vec<ClaimedEntity>,
    concurrency_limit: usize,
    reconcile_one: F,
) -> Vec<BatchOutcome>
where
    F: Fn(ClaimedEntity) -> Fut,
    Fut: std::future::Future<Output = anyhow::Result<()>>,
{
    stream::iter(claimed)
        .map(|entity| {
            let fut = reconcile_one(entity.clone());
            async move { (entity, fut.await) }
        })
        .buffer_unordered(concurrency_limit.max(1))
        .then(|(entity, result)| async {
            match &result {
                Ok(()) => {
                    let _ = record_success(pool, entity.tenant_id, &entity.entity_name, entity.desired_version).await;
                }
                Err(err) => {
                    let _ = record_failure(pool, entity.tenant_id, &entity.entity_name, err).await;
                }
            }
            BatchOutcome { entity, result }
        })
        .collect()
        .await
}

/// §6.4 — canary → wave rollout: `wave_size(total, 0)` is 1-2 tenants, then 5%, 25%, 100%. Each
/// wave's slice is *cumulative* (includes every tenant from prior waves), matching "1-2 tenant
/// nội bộ → 5% → 25% → 100%" read as an expanding rollout, not four disjoint groups.
pub fn wave_size(total_tenants: usize, wave: u32) -> usize {
    if total_tenants == 0 {
        return 0;
    }
    let canary = total_tenants.min(2);
    match wave {
        0 => canary,
        1 => canary.max(((total_tenants as f64) * 0.05).ceil() as usize),
        2 => canary.max(((total_tenants as f64) * 0.25).ceil() as usize),
        _ => total_tenants,
    }
    .min(total_tenants)
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum WaveDecision {
    /// `desired_version` was bumped for this many tenants (this wave's slice, in order).
    Advanced { tenants_in_wave: usize },
    /// The previous wave's error rate exceeded `max_error_rate` — nothing was bumped.
    Halted { error_rate_percent: u32 },
}

/// Checks the *previous* wave's outcome (skipped for wave 0, which has nothing prior to judge)
/// before advancing `desired_version` to `pack_version` for `tenant_ids[..wave_size(wave)]`.
/// `tenant_ids` order is the caller's canary selection — put the tenants you trust least to
/// reveal a bad pack first.
pub async fn advance_wave(
    pool: &PgPool,
    entity_name: &str,
    pack_version: i64,
    tenant_ids: &[Uuid],
    wave: u32,
    max_error_rate_percent: u32,
) -> anyhow::Result<WaveDecision> {
    if wave > 0 {
        let prior_size = wave_size(tenant_ids.len(), wave - 1);
        let prior_cohort = &tenant_ids[..prior_size];
        let error_rate = cohort_error_rate_percent(pool, entity_name, pack_version, prior_cohort).await?;
        if error_rate > max_error_rate_percent {
            return Ok(WaveDecision::Halted {
                error_rate_percent: error_rate,
            });
        }
    }

    let size = wave_size(tenant_ids.len(), wave);
    let cohort = &tenant_ids[..size];
    sqlx::query(
        "INSERT INTO reconciler_entity_deployments (tenant_id, entity_name, desired_version) \
         SELECT unnest($1::uuid[]), $2, $3 \
         ON CONFLICT (tenant_id, entity_name) DO UPDATE \
         SET desired_version = EXCLUDED.desired_version, status = 'pending', attempts = 0, \
             failure_class = NULL, last_error = NULL, updated_at = now() \
         WHERE reconciler_entity_deployments.desired_version < EXCLUDED.desired_version",
    )
    .bind(cohort)
    .bind(entity_name)
    .bind(pack_version)
    .execute(pool)
    .await?;

    Ok(WaveDecision::Advanced { tenants_in_wave: size })
}

async fn cohort_error_rate_percent(
    pool: &PgPool,
    entity_name: &str,
    pack_version: i64,
    cohort: &[Uuid],
) -> anyhow::Result<u32> {
    if cohort.is_empty() {
        return Ok(0);
    }
    let (total, blocked): (i64, i64) = sqlx::query_as(
        "SELECT count(*), count(*) FILTER (WHERE status = 'blocked') \
         FROM reconciler_entity_deployments \
         WHERE entity_name = $1 AND desired_version = $2 AND tenant_id = ANY($3)",
    )
    .bind(entity_name)
    .bind(pack_version)
    .bind(cohort)
    .fetch_one(pool)
    .await?;
    if total == 0 {
        return Ok(0);
    }
    Ok(((blocked as f64 / total as f64) * 100.0).round() as u32)
}

#[cfg(test)]
mod tests;
