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
//! 2. For each upstream: log into `LOGIN_URL` (that service's own `POST /auth/login`) as
//!    `SERVICE_EMAIL`/`SERVICE_PASSWORD`, then `GET {METADATA_URL}` (bearer the token just
//!    obtained) to discover its entities, and connect one `GrpcBackend` to `GRPC_ADDR`
//!    (`schema_builder.rs`) — see `metap_grpc::ServiceTokenSource` for how that login is kept
//!    fresh for the life of this process, not just at boot.
//! 3. Register every discovered entity into one shared `MetadataRegistry` (fails fast on a
//!    duplicate name across upstreams — `MetadataRegistry::register`'s own check) and build a
//!    `CompositeBackend` mapping each entity name back to the `GrpcBackend` of the upstream that
//!    owns it.
//! 4. `metap_graphql::build_schema` over that registry/backend — one schema serving every
//!    upstream's entities in a single GraphQL endpoint.
//! 5. Serve a minimal `axum` app of its own (`server.rs`) — `GET /health`, `POST /graphql`,
//!    `GET /graphql/playground` (non-production only).
//!
//! Thin wrapper over `metap_graphql_gateway`'s library modules — see that crate's `src/lib.rs`
//! for why this is split out (its own e2e test needs to call `schema_builder::build` directly).

use metap_graphql_gateway::{config, schema_builder, server};

#[tokio::main]
async fn main() -> anyhow::Result<()> {
    metap_infra::init_tracing();
    let config = config::GatewayConfig::from_env()?;

    tracing::info!(upstreams = config.upstreams.len(), "discovering upstream schemas...");
    let built = schema_builder::build(
        &config.upstreams,
        metap_graphql::SchemaLimits {
            depth: config.graphql_max_depth,
            complexity: config.graphql_max_complexity,
        },
    )
    .await?;
    tracing::info!(entities = built.entity_count, "schema built, starting server");

    server::serve(config, built).await
}
