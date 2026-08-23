//! §5.8 — a `Cost::Heavy` op whose process died leaves the entity stuck `Migrating` forever
//! (permanent 503s for it, per the design's Router-integration intent — not wired up yet, see
//! `docs/features/04-table-per-entity.md`). Lease + heartbeat (`reconciler_entity_status.
//! lease_expires_at`, set by `status::set_status`) detects this; **never rollback** (DDL can't
//! be rolled back once run) — `requeue` so the next `reconcile()` call picks up exactly where
//! level-triggered reconciliation always resumes from (`introspect(actual)` sees whatever
//! state the dead process left behind and diffs from there), up to a retry cap past which the
//! entity is marked `Error` instead of silently stuck.

use sqlx::PgPool;
use uuid::Uuid;

const MAX_RETRY_ATTEMPTS: i32 = 5;

#[derive(Debug, Clone)]
pub struct StuckEntity {
    pub tenant_id: Uuid,
    pub entity_name: String,
    pub attempts: i32,
}

pub async fn find_stuck_entities(pool: &PgPool) -> anyhow::Result<Vec<StuckEntity>> {
    let rows: Vec<(Uuid, String, i32)> = sqlx::query_as(
        "SELECT tenant_id, entity_name, attempts FROM reconciler_entity_status
         WHERE status = 'migrating' AND lease_expires_at IS NOT NULL AND lease_expires_at < now()",
    )
    .fetch_all(pool)
    .await?;
    Ok(rows
        .into_iter()
        .map(|(tenant_id, entity_name, attempts)| StuckEntity {
            tenant_id,
            entity_name,
            attempts,
        })
        .collect())
}

/// Clears the stale lease so a subsequent `reconcile()` call is free to re-acquire the advisory
/// lock and pick the entity back up (requeue), unless `attempts` is already past the retry cap —
/// then it's marked `Error` instead, so a stuck entity surfaces as an alertable state rather
/// than silently retrying forever.
pub async fn requeue_or_fail(pool: &PgPool, stuck: &StuckEntity) -> anyhow::Result<()> {
    if stuck.attempts + 1 > MAX_RETRY_ATTEMPTS {
        sqlx::query(
            "UPDATE reconciler_entity_status SET status = 'error', attempts = attempts + 1, \
             last_error = $3, lease_owner = NULL, lease_expires_at = NULL, updated_at = now() \
             WHERE tenant_id = $1 AND entity_name = $2",
        )
        .bind(stuck.tenant_id)
        .bind(&stuck.entity_name)
        .bind(format!(
            "watchdog: exceeded {MAX_RETRY_ATTEMPTS} retry attempts on a stuck Migrating lease — needs manual intervention"
        ))
        .execute(pool)
        .await?;
    } else {
        sqlx::query(
            "UPDATE reconciler_entity_status SET attempts = attempts + 1, lease_owner = NULL, \
             lease_expires_at = NULL, updated_at = now() WHERE tenant_id = $1 AND entity_name = $2",
        )
        .bind(stuck.tenant_id)
        .bind(&stuck.entity_name)
        .execute(pool)
        .await?;
    }
    Ok(())
}
