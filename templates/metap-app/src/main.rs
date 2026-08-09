//! Boot sequence for {{project-name}}: register entities, validate references, drift check,
//! index reconcile, serve. Everything here comes from `metap::prelude` — see
//! `example_entity.rs` for a starting point to replace with your own entity, and
//! `metap`'s own doc comment (`crates/metap/src/lib.rs` in the metap repo) for what else
//! is reachable through its namespaced modules (`metap::query`, `metap::workflow`, etc.).
//!
//! Reads config from the environment (or a `.env` file in the current directory — see
//! `.env.example`). Run from this directory so that resolves the way you expect.

mod example_entity;

use std::sync::Arc;

use jsonwebtoken::DecodingKey;
use metap::prelude::*;

#[tokio::main]
async fn main() -> anyhow::Result<()> {
    let config = load_config()?;

    eprintln!("[{{project-name}}] connecting to postgres...");
    let pool = connect_db(&config.database_url).await?;

    let mut registry = MetadataRegistry::new();
    registry.register(example_entity::example_entity())?;
    registry.validate_references()?;

    let entities = registry.list_entities();
    check_metadata_drift(&pool, &entities).await;
    reconcile_indexes(&pool, &entities).await;

    let permissions = PermissionService::new(Box::new(PostgresPolicyStore::new(pool.clone())));

    let public_key_pem = std::fs::read(&config.auth_jwt_public_key_path)
        .map_err(|e| anyhow::anyhow!("failed to read {}: {e}", config.auth_jwt_public_key_path))?;
    let decoding_key = DecodingKey::from_rsa_pem(&public_key_pem)?;

    let state = AppState::new(pool, Arc::new(registry), Arc::new(permissions), decoding_key);
    let router = build_router(state, &config.cors_origins);

    let addr = format!("{}:{}", config.host, config.port);
    let listener = tokio::net::TcpListener::bind(&addr).await?;
    eprintln!("[{{project-name}}] listening on http://{addr}");

    // `build_router`'s rate-limit layer keys on peer IP via `ConnectInfo<SocketAddr>` —
    // plain `into_make_service()` wouldn't populate that extension and every request would
    // fail rate-limit key extraction.
    axum::serve(
        listener,
        router.into_make_service_with_connect_info::<std::net::SocketAddr>(),
    )
    .with_graceful_shutdown(shutdown_signal())
    .await?;

    Ok(())
}

async fn shutdown_signal() {
    tokio::signal::ctrl_c().await.ok();
    eprintln!("[{{project-name}}] shutdown signal received, exiting");
}
