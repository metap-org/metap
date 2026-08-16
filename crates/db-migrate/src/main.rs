//! Replaces `packages/core`'s `db:generate`/`db:migrate` (Drizzle) for the Rust stack.
//! Applies `crates/migrations/*.sql` via `sqlx::migrate!`, which tracks applied versions in
//! its own `_sqlx_migrations` table. `crates/migrations/` is the sole source of truth for
//! schema now (the original 0000-0005 files are the same SQL Drizzle once generated from
//! `packages/core/src/infra/db/schema.ts`, copied here verbatim when `packages/core` was
//! removed, and `_sqlx_migrations` was backfilled to
//! match once schema changes started landing here instead, 2026-08-09). No schema changes go
//! through Drizzle anymore; add new numbered `.sql` files here directly.
//!
//! **`sqlx::migrate!`'s directory scan is a compile-time proc-macro expansion, not a build
//! script with a `rerun-if-changed` on the directory** — cargo has no way to know a *new*
//! migration file should trigger recompilation unless something it already tracks (this
//! file) also changed. Adding a migration file without touching this crate's own source can
//! leave `cargo build -p db-migrate` reusing a stale cached binary that's silently missing
//! it. If a freshly-added migration doesn't show up in `_sqlx_migrations` after running this,
//! `touch src/main.rs` (or `cargo clean -p db-migrate`) and rebuild before assuming something
//! else is wrong.

#[tokio::main]
async fn main() -> anyhow::Result<()> {
    metap_infra::init_tracing();
    dotenvy::dotenv().ok();
    let database_url = std::env::var("DATABASE_URL").map_err(|_| anyhow::anyhow!("DATABASE_URL is required"))?;

    tracing::info!("connecting to postgres...");
    let pool = sqlx::postgres::PgPoolOptions::new()
        .max_connections(1)
        .connect(&database_url)
        .await?;

    tracing::info!("applying migrations from crates/migrations/...");
    sqlx::migrate!("../migrations").run(&pool).await?;

    tracing::info!("done");
    Ok(())
}
