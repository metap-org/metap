//! Per-`(tenant, entity)` reconcile status (`reconciler_entity_status`,
//! `crates/migrations/0017_reconciler_tables.sql`) — what the executor flips to `Migrating`
//! before running a `Cost::Heavy` op and back to `Active` once done (§5.6), and what the
//! watchdog (§5.8) scans for entities stuck past their lease.

use sqlx::PgPool;
use uuid::Uuid;

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum EntityStatus {
    Active,
    Migrating,
    Error,
}

impl EntityStatus {
    fn as_str(self) -> &'static str {
        match self {
            EntityStatus::Active => "active",
            EntityStatus::Migrating => "migrating",
            EntityStatus::Error => "error",
        }
    }
}

pub async fn get_status(pool: &PgPool, tenant_id: Uuid, entity_name: &str) -> anyhow::Result<EntityStatus> {
    let row: Option<String> =
        sqlx::query_scalar("SELECT status FROM reconciler_entity_status WHERE tenant_id = $1 AND entity_name = $2")
            .bind(tenant_id)
            .bind(entity_name)
            .fetch_optional(pool)
            .await?;
    Ok(match row.as_deref() {
        Some("migrating") => EntityStatus::Migrating,
        Some("error") => EntityStatus::Error,
        _ => EntityStatus::Active,
    })
}

/// Sets status and, for `Migrating`, claims a fresh lease (`lease_owner`/`lease_expires_at`) —
/// what the watchdog (§5.8) checks to tell "actively being worked on" from "died mid-op".
/// `lease_ttl` should be comfortably longer than a single `DdlOp`'s expected run time.
pub async fn set_status(
    pool: &PgPool,
    tenant_id: Uuid,
    entity_name: &str,
    status: EntityStatus,
    lease_owner: Uuid,
    lease_ttl: chrono::Duration,
) -> anyhow::Result<()> {
    let lease_expires_at = (status == EntityStatus::Migrating).then(|| chrono::Utc::now() + lease_ttl);
    sqlx::query(
        "INSERT INTO reconciler_entity_status (tenant_id, entity_name, status, lease_owner, lease_expires_at, updated_at)
         VALUES ($1, $2, $3, $4, $5, now())
         ON CONFLICT (tenant_id, entity_name) DO UPDATE
         SET status = EXCLUDED.status, lease_owner = EXCLUDED.lease_owner,
             lease_expires_at = EXCLUDED.lease_expires_at, updated_at = now()",
    )
    .bind(tenant_id)
    .bind(entity_name)
    .bind(status.as_str())
    .bind(lease_owner)
    .bind(lease_expires_at)
    .execute(pool)
    .await?;
    Ok(())
}

pub async fn record_error(pool: &PgPool, tenant_id: Uuid, entity_name: &str, error: &str) -> anyhow::Result<()> {
    sqlx::query(
        "INSERT INTO reconciler_entity_status (tenant_id, entity_name, status, attempts, last_error, updated_at)
         VALUES ($1, $2, 'error', 1, $3, now())
         ON CONFLICT (tenant_id, entity_name) DO UPDATE
         SET status = 'error', attempts = reconciler_entity_status.attempts + 1,
             last_error = EXCLUDED.last_error, updated_at = now()",
    )
    .bind(tenant_id)
    .bind(entity_name)
    .bind(error)
    .execute(pool)
    .await?;
    Ok(())
}

/// `pg_try_advisory_lock` is **session-scoped**: it locks against whichever physical connection
/// issues it, and only the *same* connection can release it. Calling this (and
/// `advisory_unlock`) against a bare `&PgPool` would silently defeat the whole point — each
/// call could be handed a different pooled connection, so the lock might never actually be
/// held across the work it's meant to guard, and `advisory_unlock` could run on a connection
/// that never held it at all (a no-op). Both take an explicit `&mut PgConnection` instead —
/// the caller (`executor::execute`) must `pool.acquire()` **one** connection and hold it for
/// the lock's entire lifetime, separate from whatever connections the actual DDL/backfill work
/// checks out from the pool.
///
/// `hashtext` is Postgres's own stable string hash, so two callers hashing the same
/// `(tenant_id, entity_name)` pair always contend for the same lock key, unlike hashing
/// client-side with a generic hasher whose output isn't guaranteed stable across process
/// restarts.
pub async fn try_advisory_lock(
    conn: &mut sqlx::PgConnection,
    tenant_id: Uuid,
    entity_name: &str,
) -> anyhow::Result<bool> {
    let key = format!("{tenant_id}:{entity_name}");
    let acquired: bool = sqlx::query_scalar("SELECT pg_try_advisory_lock(hashtext($1)::bigint)")
        .bind(key)
        .fetch_one(conn)
        .await?;
    Ok(acquired)
}

pub async fn advisory_unlock(conn: &mut sqlx::PgConnection, tenant_id: Uuid, entity_name: &str) -> anyhow::Result<()> {
    let key = format!("{tenant_id}:{entity_name}");
    sqlx::query("SELECT pg_advisory_unlock(hashtext($1)::bigint)")
        .bind(key)
        .execute(conn)
        .await?;
    Ok(())
}
