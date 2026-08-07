//! The boot sequence the old `apps/crm/src/main.ts` + `app.ts`'s `buildApp` used to
//! document (register entities, validate references, drift check, index reconcile, serve)
//! reassembled from the `crates/metap-*` crates. Run from this crate's own directory
//! (`crates/crm-server/`) so `.env`/`keys/` resolution works — `pnpm dev:rs` does this via
//! `cd`; see `metap-infra/src/config.rs` for the `.env` resolution itself.

mod customer_entity;

use std::sync::Arc;

use jsonwebtoken::DecodingKey;
use metap_http::{build_router, AppState};
use metap_metadata::MetadataRegistry;
use metap_permission::PermissionService;

#[tokio::main]
async fn main() -> anyhow::Result<()> {
    let config = metap_infra::load_config()?;

    eprintln!("[crm-server] connecting to postgres...");
    let pool = metap_infra::connect_db(&config.database_url).await?;

    let mut registry = MetadataRegistry::new();
    registry.register(customer_entity::customer_entity())?;
    registry.validate_references()?;

    let entities = registry.list_entities();
    metap_peripherals::check_metadata_drift(&pool, &entities).await;
    metap_peripherals::reconcile_indexes(&pool, &entities).await;

    let permissions = PermissionService::new(Box::new(metap_permission::PostgresPolicyStore::new(pool.clone())));

    let public_key_pem = std::fs::read(&config.auth_jwt_public_key_path)
        .map_err(|e| anyhow::anyhow!("failed to read {}: {e}", config.auth_jwt_public_key_path))?;
    let decoding_key = DecodingKey::from_rsa_pem(&public_key_pem)?;

    let state = AppState::new(pool, Arc::new(registry), Arc::new(permissions), decoding_key);
    let router = build_router(state, &config.cors_origins);

    let addr = format!("{}:{}", config.host, config.port);
    let listener = tokio::net::TcpListener::bind(&addr).await?;
    eprintln!("[crm-server] listening on http://{addr}");

    axum::serve(listener, router)
        .with_graceful_shutdown(shutdown_signal())
        .await?;

    Ok(())
}

async fn shutdown_signal() {
    tokio::signal::ctrl_c().await.ok();
    eprintln!("[crm-server] shutdown signal received, exiting");
}
