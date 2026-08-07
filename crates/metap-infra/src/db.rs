//! Thin wrapper around a `sqlx::PgPool` — mirrors `packages/core/src/infra/db/client.ts`'s
//! role (the one place a Postgres connection pool is created), not a Repository/ORM layer.
//! Per `docs/architecture-review-2026-08-07.md` Part 1's Repository finding, this crate
//! deliberately does not introduce a generic repository abstraction — there's no second
//! datastore trigger yet, and the real Postgres-dialect seam lives in `QueryPlanner`, not
//! here.

use sqlx::postgres::PgPoolOptions;
use sqlx::PgPool;

pub async fn connect(database_url: &str) -> anyhow::Result<PgPool> {
    Ok(PgPoolOptions::new()
        .max_connections(5)
        .connect(database_url)
        .await?)
}

/// Mirrors `packages/core/src/core/health/health-service.ts`'s `checkDatabase`.
pub async fn health_check(pool: &PgPool) -> bool {
    sqlx::query("select 1").execute(pool).await.is_ok()
}
