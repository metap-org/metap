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
use metap::prelude::*;

#[tokio::main]
async fn main() -> anyhow::Result<()> {
    let config = load_config()?;

    // `bootstrap_platform` (`metap-app`) connects to Postgres, builds the tenant `Router` (with
    // whichever `SecretStore` backend is configured), `PermissionService`, and the JWT keypair —
    // see that crate's own doc comment for the full recipe, and for how to write a custom
    // route/handler beyond entity declaration (tenant-scoped DB access, permission-aware
    // handlers, publish/subscribe events).
    let PlatformParts {
        pool,
        router,
        permissions,
        decoding_key,
        private_key_pem,
    } = bootstrap_platform(&config).await?;

    let mut registry = MetadataRegistry::new();
    registry.register(example_entity::example_entity())?;
    registry.validate_references()?;
    let metadata_base = Arc::new(registry);

    let entities = metadata_base.list_entities();
    check_metadata_drift(&pool, &entities).await;
    reconcile_indexes(&pool, &entities).await;

    let metadata = Arc::new(ArcSwap::new(metadata_base.clone()));

    let state = AppState::new(
        pool,
        metadata_base,
        metadata,
        permissions,
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
    //   its own `tokio::spawn` alongside the `metap::runtime::serve::run` call below — a second
    //   port, not merged into this router (see that crate's `serve` doc comment for why).
    let router = build_router(state, &config.cors_origins, Router::new());

    let addr = format!("{}:{}", config.host, config.port);
    eprintln!("[{{project-name}}] listening on http://{addr}");

    // `metap::runtime::serve::run` binds the listener, serves, and waits for Ctrl+C/SIGTERM —
    // `build_router`'s rate-limit layer keys on peer IP via `ConnectInfo<SocketAddr>`, so plain
    // `into_make_service()` wouldn't populate that extension and every request would fail
    // rate-limit key extraction.
    metap::runtime::serve::run(
        &addr,
        router.into_make_service_with_connect_info::<std::net::SocketAddr>(),
    )
    .await?;

    Ok(())
}
