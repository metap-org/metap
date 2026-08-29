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

use arc_swap::ArcSwap;
use axum::Router;
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
    let metadata_base = Arc::new(registry);

    let entities = metadata_base.list_entities();
    check_metadata_drift(&pool, &entities).await;
    reconcile_indexes(&pool, &entities).await;

    let metadata = Arc::new(ArcSwap::new(metadata_base.clone()));

    // `Router` (`metap::control`) is the multi-tenant seam every tenant-scoped query goes
    // through — built once here and shared with `PostgresPolicyStore` below so both use the
    // same `RegistryCache`. `EnvStore` here, not `VaultStore` — this starter template doesn't
    // wire in Vault by default; add `metap::control::VaultStore` yourself if you need a
    // `DedicatedDb`-strategy tenant's DSN to come from Vault instead of an env var.
    let tenant_registry = Arc::new(metap::control::PostgresTenantRegistry::new(pool.clone()));
    let router = metap::control::Router::new(
        pool.clone(),
        metap::control::RegistryCache::new(tenant_registry),
        Arc::new(metap::control::EnvStore),
    );

    let permissions = PermissionService::new(Box::new(PostgresPolicyStore::new(router.clone())));

    let public_key_pem = std::fs::read(&config.auth_jwt_public_key_path)
        .map_err(|e| anyhow::anyhow!("failed to read {}: {e}", config.auth_jwt_public_key_path))?;
    let decoding_key = DecodingKey::from_rsa_pem(&public_key_pem)?;

    // Needed only for POST /auth/login (metap_peripherals::mint_jwt) — this binary issues
    // tokens, not just verifies them, so both halves of the keypair are load-bearing.
    let private_key_pem = std::fs::read_to_string(&config.auth_jwt_private_key_path).map_err(|e| {
        anyhow::anyhow!("failed to read {}: {e}", config.auth_jwt_private_key_path)
    })?;

    let state = AppState::new(
        pool,
        metadata_base,
        metadata,
        Arc::new(permissions),
        decoding_key,
        private_key_pem,
        router,
    );
    // `Router::new()` — this template doesn't wire in `metap-lowcode-http`'s DB-authored
    // entity control plane by default; a single code-authored `example_entity` is the
    // starting point. Add `metap-lowcode-http` as a dependency and pass
    // `metap::lowcode_http::router()` here instead if you want that surface.
    //
    // Same opt-in shape for two more optional transports on top of REST, neither wired by
    // default here:
    // - GraphQL: add `metap-graphql-http` as a dependency and merge
    //   `metap::graphql_http::router(&state, metap::graphql::SchemaLimits::default())?` into
    //   the `extra_routes` argument below (same as `lowcode_http::router()` above) — mounts
    //   `POST /graphql`, a schema generated from this binary's own `MetadataRegistry`.
    // - gRPC: add `metap-grpc` as a dependency and spawn `metap::grpc::serve(grpc_addr,
    //   metap::grpc::GrpcRecordService::new(state.crud.clone(), auth_config), tls_config)` in
    //   its own `tokio::spawn` alongside the `axum::serve` call below — a second port, not
    //   merged into this router (see that crate's `serve` doc comment for why).
    let router = build_router(state, &config.cors_origins, Router::new());

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
