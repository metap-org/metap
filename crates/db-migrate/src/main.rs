//! Replaces `packages/core`'s `db:generate`/`db:migrate` (Drizzle) for the Rust stack.
//! Applies `crates/migrations/*.sql` — the same SQL Drizzle originally generated from
//! `packages/core/src/infra/db/schema.ts`, copied here verbatim when `packages/core` was
//! removed (see `docs/rust-core-viability.md`) — via `sqlx::migrate!`, which tracks applied
//! versions in its own `_sqlx_migrations` table.
//!
//! **Only for a fresh database.** The repo's existing dev Postgres already has this schema
//! applied (via Drizzle, tracked in Drizzle's own journal, not `_sqlx_migrations`) — running
//! this against it is a no-op in effect (all statements are already-applied DDL) but would
//! still try to CREATE things that exist and fail, since sqlx has no record of them. This
//! tool is for bootstrapping a database that doesn't have the schema yet: CI, a fresh dev
//! setup, or a new environment. There is currently no reconciliation between Drizzle's and
//! sqlx's migration-tracking tables for the *existing* dev database — not needed unless a
//! new migration is written after this point and needs to land on both.
//!
//! No schema changes go through Drizzle from here on — `crates/migrations/` is the source of
//! truth going forward; add new numbered `.sql` files here directly.

#[tokio::main]
async fn main() -> anyhow::Result<()> {
    dotenvy::dotenv().ok();
    let database_url = std::env::var("DATABASE_URL")
        .map_err(|_| anyhow::anyhow!("DATABASE_URL is required"))?;

    eprintln!("[db-migrate] connecting to {database_url}...");
    let pool = sqlx::postgres::PgPoolOptions::new()
        .max_connections(1)
        .connect(&database_url)
        .await?;

    eprintln!("[db-migrate] applying migrations from crates/migrations/...");
    sqlx::migrate!("../migrations").run(&pool).await?;

    eprintln!("[db-migrate] done.");
    Ok(())
}
