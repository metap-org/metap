//! The boot sequence the old `apps/crm/src/main.ts` + `app.ts`'s `buildApp` used to
//! document (register entities, validate references, drift check, index reconcile, serve)
//! reassembled from the `metap` facade crate (see `crates/metap/src/lib.rs` — a thin
//! re-export layer over `crates/metap-*`, so this file only needs one dependency/import
//! instead of naming each sub-crate). Run from this crate's own directory
//! (`apps/crm-server/`) so `.env`/`keys/` resolution works — `pnpm dev:rs` does this via
//! `cd`; see `metap-infra/src/config.rs` for the `.env` resolution itself.

mod customer_entity;

use std::sync::Arc;

use jsonwebtoken::DecodingKey;
use metap::infra::{EventBus, RabbitEventBus};
use metap::prelude::*;
use tower_http::services::{ServeDir, ServeFile};

#[tokio::main]
async fn main() -> anyhow::Result<()> {
    metap::infra::init_tracing();
    let config = load_config()?;

    tracing::info!("connecting to postgres...");
    let pool = connect_db(&config.database_url).await?;

    let mut registry = MetadataRegistry::new();
    registry.register(customer_entity::customer_entity())?;
    registry.validate_references()?;

    let entities = registry.list_entities();
    check_metadata_drift(&pool, &entities).await;
    reconcile_indexes(&pool, &entities).await;

    let permissions = PermissionService::new(Box::new(PostgresPolicyStore::new(pool.clone())));

    let public_key_pem = std::fs::read(&config.auth_jwt_public_key_path)
        .map_err(|e| anyhow::anyhow!("failed to read {}: {e}", config.auth_jwt_public_key_path))?;
    let decoding_key = DecodingKey::from_rsa_pem(&public_key_pem)?;

    let state = AppState::new(pool, Arc::new(registry), Arc::new(permissions), decoding_key);
    let mut router = build_router(state, &config.cors_origins);

    if let Some(dir) = &config.static_dir {
        if std::path::Path::new(dir).is_dir() {
            tracing::info!(dir, "serving frontend static files");
            let index_html = format!("{dir}/index.html");
            // `.fallback()`, not `.not_found_service()` — the latter always forces the
            // response status to 404 (see its doc comment), which is wrong for SPA
            // client-side routes: the browser needs a real 200 to render `index.html`
            // normally instead of treating it as an error page.
            router = router.fallback_service(ServeDir::new(dir).fallback(ServeFile::new(index_html)));
        } else {
            tracing::warn!(dir, "STATIC_DIR is set but is not a directory, skipping static file serving");
        }
    }

    // Off by default — the standalone `notification-worker` binary (`pnpm
    // worker:notification:rs`) is the normal deployment shape, matching `outbox-publisher`.
    // Set NOTIFICATION_WORKER_INLINE=true to run it as a background task in this same
    // process instead, for single-process/monolithic deployments (same pattern as
    // `pnpm start`'s STATIC_DIR merging crm-fe into this binary) — both modes call the exact
    // same `notification_worker::run`, so they can't drift apart.
    let notification_worker_handle = if env_flag_enabled("NOTIFICATION_WORKER_INLINE") {
        tracing::info!("connecting notification worker to rabbitmq (inline mode)...");
        let notification_bus = RabbitEventBus::connect(&config.rabbitmq_url).await?;
        Some(tokio::spawn(async move {
            if let Err(err) = notification_worker::run(&notification_bus, shutdown_signal()).await
            {
                tracing::error!(error = %err, "notification worker exited with error");
            }
            notification_bus.close().await.ok();
        }))
    } else {
        None
    };

    let addr = format!("{}:{}", config.host, config.port);
    let listener = tokio::net::TcpListener::bind(&addr).await?;
    tracing::info!(%addr, "listening");

    // `build_router`'s rate-limit layer keys on peer IP via `ConnectInfo<SocketAddr>` — see
    // `metap_http::build_router`'s doc comment. Plain `into_make_service()` wouldn't
    // populate that extension and every request would fail rate-limit key extraction.
    axum::serve(
        listener,
        router.into_make_service_with_connect_info::<std::net::SocketAddr>(),
    )
    .with_graceful_shutdown(shutdown_signal())
    .await?;

    // Block on the inline notification worker's own shutdown branch (and its
    // `bus.close()`) instead of letting the process exit as soon as the HTTP server drains —
    // otherwise the spawned task above can be cut off mid-message.
    if let Some(handle) = notification_worker_handle {
        handle.await.ok();
    }

    Ok(())
}

async fn shutdown_signal() {
    tokio::signal::ctrl_c().await.ok();
    tracing::info!("shutdown signal received, exiting");
}

fn env_flag_enabled(name: &str) -> bool {
    std::env::var(name).is_ok_and(|v| v == "true" || v == "1")
}
