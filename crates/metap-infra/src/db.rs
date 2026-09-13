//! Thin wrapper around a `sqlx::PgPool` — mirrors `packages/core/src/infra/db/client.ts`'s
//! role (the one place a Postgres connection pool is created), not a Repository/ORM layer.
//! Per `docs/architectures/09-adr.md`, this crate
//! deliberately does not introduce a generic repository abstraction — there's no second
//! datastore trigger yet, and the real Postgres-dialect seam lives in `QueryPlanner`, not
//! here.

use std::str::FromStr;

use sqlx::postgres::{PgConnectOptions, PgPoolOptions};
use sqlx::PgPool;

pub async fn connect(database_url: &str) -> anyhow::Result<PgPool> {
    Ok(PgPoolOptions::new().max_connections(5).connect(database_url).await?)
}

/// A single-connection pool for running `sqlx::migrate!` against a database — used by
/// `db-migrate` and `metap-control::provisioning::provision_dedicated_db_tenant`, the only 2
/// places in this codebase that run migrations programmatically.
///
/// **Sets `search_path` at the connection level, not just `max_connections(1)`** — this closes a
/// real bug found live (2026-09-13) migrating a brand-new database from scratch:
/// `0028_metadata_schema.sql` moves several framework tables from `public` into a new `metadata`
/// schema and sets the *database's own default* `search_path` to `public, metadata, control` via
/// `ALTER DATABASE ... SET search_path`, expecting every later migration's unqualified table
/// references (and `0030`'s own `ALTER TABLE public._sqlx_migrations SET SCHEMA metadata`) to
/// resolve through it. But `ALTER DATABASE`'s new default only applies to *future* connections —
/// the one connection this function's caller keeps open for the entire `sqlx::migrate!().run()`
/// call already started with whatever `search_path` it had at connect time (typically just
/// `"$user", public`), so it never picks up the new default mid-run. The practical symptom: right
/// after `0030` moves `_sqlx_migrations` itself out of `public`, sqlx's own internal
/// "record this migration as applied" `INSERT` (which runs on this same session, not a fresh one)
/// fails with `relation "_sqlx_migrations" does not exist` — reproducible on *any* freshly created
/// database run through `sqlx::migrate!` from scratch, not specific to any one migration file
/// added since. Setting `search_path` as a connection-startup option here (`-c
/// search_path=public,metadata,control`, harmless before those schemas exist — Postgres resolves
/// `search_path` lazily at name-lookup time, not at `SET`/connect time) makes the *first*
/// connection already carry the search path `0028`'s `ALTER DATABASE` was trying to establish for
/// every connection after it, closing the gap without touching either already-applied,
/// checksummed migration file.
pub async fn connect_for_migrate(database_url: &str) -> anyhow::Result<PgPool> {
    let options = PgConnectOptions::from_str(database_url)?.options([("search_path", "public,metadata,control")]);
    Ok(PgPoolOptions::new().max_connections(1).connect_with(options).await?)
}

/// Mirrors `packages/core/src/core/health/health-service.ts`'s `checkDatabase`.
pub async fn health_check(pool: &PgPool) -> bool {
    sqlx::query("select 1").execute(pool).await.is_ok()
}
