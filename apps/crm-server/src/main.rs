//! The boot sequence the old `apps/crm/src/main.ts` + `app.ts`'s `buildApp` used to
//! document (register entities, validate references, drift check, index reconcile, serve)
//! reassembled from the `metap` facade crate (see `crates/metap/src/lib.rs` — a thin
//! re-export layer over `crates/metap-*`, so this file only needs one dependency/import
//! instead of naming each sub-crate). Run from this crate's own directory
//! (`apps/crm-server/`) so `.env`/`keys/` resolution works — `pnpm dev:rs` does this via
//! `cd`; see `metap-infra/src/config.rs` for the `.env` resolution itself.
//!
//! `apps/crm-server`/`apps/crm-fe` together are the demo/test app for this whole project, not
//! a real product — see `docs/features/README.md`. `src/entities/` holds every entity this
//! demo app registers, kept in one folder since none of them are a real business module (see
//! each entity's own `docs/features/*.md` brief for what it was built to prove).

mod entities;

use entities::{customer_entity, inventory_movement_entity, journal_entry_entity, sales_order_entity};

use std::sync::Arc;

use arc_swap::ArcSwap;
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

    // Code-authored entities only — fixed for the process lifetime, never touched by a
    // DB-authored publish/rollback (`docs/roadmap.md` Phase 11 / Phase A). Kept separate
    // from the merged runtime registry below so `metap-http`'s admin routes can reject a
    // DB-authored draft whose name collides with one of these, without themselves knowing
    // any entity name.
    let mut code_registry = MetadataRegistry::new();
    code_registry.register(customer_entity::customer_entity())?;
    code_registry.register(sales_order_entity::sales_order_entity())?;
    code_registry.register(inventory_movement_entity::inventory_movement_entity())?;
    code_registry.register(journal_entry_entity::journal_entry_entity())?;
    code_registry.validate_references()?;
    let metadata_base = Arc::new(code_registry);

    // DB-authored entities (`metap-lowcode`, Phase A sub-project 1/2) — every *enabled*
    // entity that has been published at least once, merged on top of the code-authored base
    // (a disabled entity stays out of the registry entirely, same as if it had never been
    // published — see `metap_lowcode::list_enabled_published`'s doc comment). Empty on a
    // fresh install; `metap-lowcode-http`'s publish/rollback/enable-toggle handlers rebuild
    // and swap this same way at runtime, so a restart is never required to pick up a change.
    let db_entities: Vec<_> = metap::lowcode::list_enabled_published(&pool)
        .await?
        .into_iter()
        .map(|(_, def)| def.to_entity_definition())
        .collect();
    let runtime_registry = metadata_base.merge_with(db_entities)?;
    // `merge_with`/`register` only run per-entity shape validation — cross-entity reference
    // checks (a DB-authored `refEntity` pointing at a code-authored entity, or vice versa)
    // need the merged registry, not `code_registry` alone, so this can't be skipped just
    // because `code_registry.validate_references()` already ran above.
    runtime_registry.validate_references()?;

    let entities = runtime_registry.list_entities();
    check_metadata_drift(&pool, &entities).await;
    reconcile_indexes(&pool, &entities).await;

    let metadata = Arc::new(ArcSwap::new(Arc::new(runtime_registry)));
    let permissions = PermissionService::new(Box::new(PostgresPolicyStore::new(pool.clone())));

    let public_key_pem = std::fs::read(&config.auth_jwt_public_key_path)
        .map_err(|e| anyhow::anyhow!("failed to read {}: {e}", config.auth_jwt_public_key_path))?;
    let decoding_key = DecodingKey::from_rsa_pem(&public_key_pem)?;

    // Needed only for POST /auth/login (metap_peripherals::mint_jwt) — crm-server issues
    // tokens now, not just verifies them, so both halves of the keypair are load-bearing.
    let private_key_pem = std::fs::read_to_string(&config.auth_jwt_private_key_path)
        .map_err(|e| anyhow::anyhow!("failed to read {}: {e}", config.auth_jwt_private_key_path))?;

    let state = AppState::new(
        pool,
        metadata_base,
        metadata,
        Arc::new(permissions),
        decoding_key,
        private_key_pem,
    );
    // `metap::lowcode_http::router()` is the low-code control plane's admin API
    // (`docs/roadmap.md` Phase 11 / Phase A) — an optional platform capability, not core;
    // `build_router` itself has zero knowledge of it (see that function's doc comment). This
    // demo app opts in; a downstream project that doesn't want DB-authored entities can pass
    // `Router::new()` here instead and never link `metap-lowcode`/`metap-lowcode-http` in.
    let mut router = build_router(state, &config.cors_origins, metap::lowcode_http::router());

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
            tracing::warn!(
                dir,
                "STATIC_DIR is set but is not a directory, skipping static file serving"
            );
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
            if let Err(err) = notification_worker::run(&notification_bus, shutdown_signal()).await {
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
