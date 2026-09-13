//! `graphql-gateway` — the real BFF: a GraphQL schema aggregated across every separately-deployed
//! microservice named in its own config (`../metap-demo-jira` + `../metap-demo-crm`, in this repo's
//! demo setup), not one binary's own entities. That's the distinction from plain
//! `metap-graphql-http::router()` mounted directly into `jira-server`/`crm-server` (Phase 49) —
//! those each serve GraphQL for *their own* entities only; a query against either of them can
//! never touch the other service's data. This binary owns no entity of its own, no Postgres, no
//! `CrudService` — every record read/write it serves is a remote gRPC call
//! (`metap_grpc::GrpcBackend`) to whichever upstream actually owns that entity
//! (`metap_graphql::CompositeBackend` routes by entity name — see `schema_builder.rs`).
//!
//! Boot sequence:
//! 1. Read `UPSTREAM_<N>_{NAME,GRPC_ADDR,METADATA_URL,LOGIN_URL,SERVICE_EMAIL,SERVICE_PASSWORD}`
//!    env vars (`config.rs`), N = 1, 2, ... until `_NAME` is missing.
//! 2. Build a `GatewaySchemaCache` (`schema_builder.rs`) — no I/O yet, just one `UpstreamCache`
//!    per configured upstream — and warm it with one `.current()` call: for each upstream, log
//!    into `LOGIN_URL` (that service's own `POST /auth/login`) as
//!    `SERVICE_EMAIL`/`SERVICE_PASSWORD`, `GET {METADATA_URL}` (bearer the token just obtained)
//!    to discover its entities, and connect one `GrpcBackend` to `GRPC_ADDR` — see
//!    `metap_grpc::ServiceTokenSource` for how that login is kept fresh for the life of this
//!    process, not just at boot. **An upstream that fails here no longer fails boot** (audit 04
//!    finding B1) — it just contributes nothing to this first schema, and is retried on every
//!    later `.current()` call once its TTL window elapses.
//! 3. Every discovered entity is registered into one composite `MetadataRegistry` (a duplicate
//!    name across upstreams is dropped and logged, not fatal) behind a `CompositeBackend` mapping
//!    each entity name back to the `GrpcBackend` of the upstream that owns it — rebuilt from
//!    scratch on every `.current()` call whose TTL has elapsed, not just once at boot.
//! 4. Serve a minimal `axum` app of its own (`server.rs`) — `GET /health`, `POST /graphql`,
//!    `GET /graphql/playground` (non-production only). Every request re-checks
//!    `GatewaySchemaCache::current()` first, which is how a newly-published low-code entity or a
//!    recovered upstream shows up without a restart.
//!
//! Thin wrapper over `metap_graphql_gateway`'s library modules — see that crate's `src/lib.rs`
//! for why this is split out (its own e2e test needs to call `schema_builder::build` directly).

use metap_graphql_gateway::{config, schema_builder, server};

#[tokio::main]
async fn main() -> anyhow::Result<()> {
    metap_infra::init_tracing();
    let config = config::GatewayConfig::from_env()?;

    tracing::info!(upstreams = config.upstreams.len(), "discovering upstream schemas...");
    let cache = schema_builder::build(
        &config.upstreams,
        metap_graphql::SchemaLimits {
            depth: config.graphql_max_depth,
            complexity: config.graphql_max_complexity,
        },
    )
    .await?;
    // Warm the cache once so boot logs the real outcome — but its failure (a down upstream) no
    // longer fails `main` itself, which is the whole point of this fix.
    let snapshot = cache.current().await;
    let degraded = snapshot.health.upstreams.iter().filter(|u| !u.reachable).count();
    tracing::info!(
        entities = snapshot.entity_count,
        upstreams = snapshot.health.upstreams.len(),
        degraded,
        "schema built, starting server"
    );

    server::serve(config, cache).await
}
