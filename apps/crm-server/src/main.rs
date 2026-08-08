//! The boot sequence the old `apps/crm/src/main.ts` + `app.ts`'s `buildApp` used to
//! document (register entities, validate references, drift check, index reconcile, serve)
//! reassembled from the `crates/metap-*` crates. Run from this crate's own directory
//! (`apps/crm-server/`) so `.env`/`keys/` resolution works — `pnpm dev:rs` does this via
//! `cd`; see `metap-infra/src/config.rs` for the `.env` resolution itself.

mod customer_entity;

use std::sync::Arc;

use jsonwebtoken::DecodingKey;
use metap_http::{build_router, AppState};
use metap_metadata::MetadataRegistry;
use metap_permission::PermissionService;
use tower_http::services::{ServeDir, ServeFile};

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
    let mut router = build_router(state, &config.cors_origins);

    if let Some(dir) = &config.static_dir {
        if std::path::Path::new(dir).is_dir() {
            eprintln!("[crm-server] serving frontend static files from {dir}");
            let index_html = format!("{dir}/index.html");
            // `.fallback()`, not `.not_found_service()` — the latter always forces the
            // response status to 404 (see its doc comment), which is wrong for SPA
            // client-side routes: the browser needs a real 200 to render `index.html`
            // normally instead of treating it as an error page.
            router = router.fallback_service(ServeDir::new(dir).fallback(ServeFile::new(index_html)));
        } else {
            eprintln!("[crm-server] STATIC_DIR={dir} is set but is not a directory, skipping static file serving");
        }
    }

    let addr = format!("{}:{}", config.host, config.port);
    let listener = tokio::net::TcpListener::bind(&addr).await?;
    eprintln!("[crm-server] listening on http://{addr}");

    // `build_router`'s rate-limit layer keys on peer IP via `ConnectInfo<SocketAddr>` — see
    // `metap_http::build_router`'s doc comment. Plain `into_make_service()` wouldn't
    // populate that extension and every request would fail rate-limit key extraction.
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
    eprintln!("[crm-server] shutdown signal received, exiting");
}
