//! The gateway's own minimal `axum` app — deliberately not `metap_http::build_router`/
//! `AppState`, which are tightly coupled to a Postgres pool + `CrudService` this binary has
//! neither of (see this crate's `main.rs` doc comment; `metap-http` is a dev-only dependency
//! here, used just by `tests/gateway_e2e_postgres.rs`'s upstream harness, not by this binary
//! itself). Reuses only the standalone-safe pieces: `metap_runtime::security_headers::security_headers`
//! (a plain middleware fn, no `AppState` dependency — moved out of `metap-http` 2026-09-02
//! specifically so this crate didn't have to pull in all of `metap-http` for just this one
//! function) and `metap_graphql_http::playground_router` (generalized to `Router<S>` for exactly
//! this reason).
//!
//! **Auth here decodes the caller's token, then forwards it verbatim to whichever upstream a
//! resolver ends up calling.** A request must carry a Bearer token that decodes against this
//! gateway's own keypair to reach `/graphql` at all; `authenticate` keeps that raw token on
//! `RequestContext::forwarded_bearer_token`, and `GrpcBackend::signed_request`
//! (`metap-grpc/src/client.rs`) prefers it over its configured `ServiceTokenSource` when present. This
//! is what lets a mutation through the gateway enforce the REAL caller's own permissions at the
//! upstream, not a shared service account's — but it only works because the gateway and every
//! upstream it talks to verify against the SAME signing keypair (true of every
//! `metap-demo-waf` service today; see that repo's `graphql-gateway/.env.example`). A deployment
//! where the gateway and its upstreams don't share a keypair would need a JWKS-based re-mint
//! instead of this plain forward — out of scope here, `metap-jwks` exists for that case.
//! No role/permission check happens in this gateway itself either way — real enforcement always
//! happens where it already did before this gateway existed, inside each upstream's own
//! `CrudService`/`PermissionService`, once the gRPC call lands there.

use std::sync::Arc;

use async_graphql_axum::{GraphQLRequest, GraphQLResponse};
use axum::extract::State;
use axum::http::{header, HeaderMap, Method};
use axum::response::{IntoResponse, Response};
use axum::routing::{get, post};
use axum::Router;
use jsonwebtoken::DecodingKey;
use metap_crud::RecordBackend;
use metap_graphql::{with_request_data, Schema};
use metap_peripherals::decode_access_token;
use metap_permission::RequestContext;

use crate::config::GatewayConfig;
use crate::schema_builder::BuiltSchema;

#[derive(Clone)]
struct GatewayState {
    schema: Arc<Schema>,
    backend: Arc<dyn RecordBackend>,
    decoding_key: Arc<DecodingKey>,
}

/// Same `{"error":{"code":...,"message":...}}` shape every other axum surface in this project
/// uses (`metap_runtime::http_error::service_error_response`) — this gateway used to hand-roll a
/// plain-text 401 body instead, the one real inconsistency found reviewing `metap-http/src/error.rs`
/// for reuse (2026-08-31). Uses `metap-runtime` directly rather than `metap_http::error`'s
/// re-export of the same function — this crate happens to depend on `metap-http` too today, but
/// the point of moving these 2 functions to `metap-runtime` was so a binary that *doesn't* (a
/// from-scratch custom router, e.g. a future `../metap-demo-waf` admin API) gets the same shape
/// without the heavier dependency.
fn unauthorized(message: &str) -> Box<Response> {
    Box::new(metap_runtime::http_error::service_error_response(
        401,
        "unauthorized",
        Some(message),
        None,
    ))
}

/// Decodes the caller's Bearer token against this gateway's own keypair — no role/permission
/// check here (see this module's doc comment for why: real enforcement happens downstream, once
/// a call reaches its owning upstream). The `Err` is boxed (clippy's `result_large_err`) since a
/// full `Response` is much larger than the `Ok` variant.
fn authenticate(headers: &HeaderMap, decoding_key: &DecodingKey) -> Result<RequestContext, Box<Response>> {
    let raw = headers
        .get(header::AUTHORIZATION)
        .and_then(|v| v.to_str().ok())
        .ok_or_else(|| unauthorized("missing authorization header"))?;
    let token = metap_runtime::bearer::parse_bearer(raw)
        .ok_or_else(|| unauthorized("authorization header must be a Bearer token"))?;
    let claims = decode_access_token(token, decoding_key, 20).map_err(|_| unauthorized("invalid or expired token"))?;
    Ok(RequestContext {
        tenant_id: claims.tenant_id,
        user_id: Some(claims.sub),
        roles: None,
        function_id: claims.function_id,
        context_attributes: None,
        forwarded_bearer_token: Some(token.to_string()),
    })
}

async fn graphql_handler(State(state): State<GatewayState>, headers: HeaderMap, req: GraphQLRequest) -> Response {
    let context = match authenticate(&headers, &state.decoding_key) {
        Ok(context) => context,
        Err(response) => return *response,
    };
    let request = with_request_data(req.into_inner(), state.backend.clone(), context);
    GraphQLResponse::from(state.schema.execute(request).await).into_response()
}

async fn health() -> &'static str {
    "ok"
}

pub async fn serve(config: GatewayConfig, built: BuiltSchema) -> anyhow::Result<()> {
    let decoding_key = DecodingKey::from_rsa_pem(&config.auth_public_key_pem)?;
    let state = GatewayState {
        schema: built.schema,
        backend: built.backend,
        decoding_key: Arc::new(decoding_key),
    };

    let cors = metap_runtime::cors::build(
        &config.cors_origins,
        &[Method::GET, Method::POST],
        &[header::AUTHORIZATION, header::CONTENT_TYPE, header::ACCEPT],
    );

    let mut app: Router<GatewayState> = Router::new()
        .route("/health", get(health))
        .route("/graphql", post(graphql_handler));

    // Same "unauthenticated static HTML, gate by env instead of by the crate itself" convention
    // `../metap-demo-jira/src/main.rs` already established for this exact router.
    if !config.is_production {
        app = app.merge(metap_graphql_http::playground_router::<GatewayState>("/graphql"));
    }

    // Rate-limit/tracing-span/request-id — this gateway had none of these at all before
    // (2026-08-31, `docs/features/08-metap-runtime-common-crate.md`'s 4th pass), unlike
    // `metap-http::build_router`'s own copy of the same defaults. Same layer order
    // `metap-http` uses: `request_id` outermost, then `trace`, then `security_headers`, then
    // `request_context`, then `rate_limit`, then `cors` innermost.
    let rate_limit = metap_runtime::rate_limit::build(200, 300);
    let trace = metap_runtime::trace::build();

    let app = app
        .layer(cors)
        .layer(rate_limit)
        .layer(axum::middleware::from_fn(
            metap_runtime::request_context::request_context,
        ))
        .layer(axum::middleware::from_fn(
            metap_runtime::security_headers::security_headers,
        ))
        .layer(trace)
        .layer(axum::middleware::from_fn(
            metap_runtime::request_id::generate_request_ids,
        ))
        .with_state(state);

    let addr = format!("{}:{}", config.host, config.port);

    // `rate_limit`'s `PeerIpKeyExtractor` reads the connection's peer address from the
    // `ConnectInfo<SocketAddr>` extension — see `metap_runtime::rate_limit::build`'s doc
    // comment — so this must serve via `into_make_service_with_connect_info`, not plain
    // `into_make_service()`.
    metap_runtime::serve::run(&addr, app.into_make_service_with_connect_info::<std::net::SocketAddr>()).await?;
    Ok(())
}
