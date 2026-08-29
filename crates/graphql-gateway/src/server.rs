//! The gateway's own minimal `axum` app — deliberately not `metap_http::build_router`/
//! `AppState`, which are tightly coupled to a Postgres pool + `CrudService` this binary has
//! neither of (see this crate's `main.rs` doc comment). Reuses only the standalone-safe pieces:
//! `metap_http::security_headers::security_headers` (a plain middleware fn, no `AppState`
//! dependency) and `metap_graphql_http::playground_router` (generalized to `Router<S>` for
//! exactly this reason).
//!
//! **Auth here is decode-only, not a source of downstream identity.** A request must carry a
//! Bearer token that decodes against this gateway's own keypair to reach `/graphql` at all — but
//! the `RequestContext` built from it is never checked for roles/permissions here, and is inert
//! once it reaches a resolver: every `RecordBackend` call downstream (`GrpcBackend`) authenticates
//! to its upstream as that upstream's own fixed `service_jwt`, regardless of who the caller was.
//! Real permission enforcement happens where it already did before this gateway existed — inside
//! each upstream's own `CrudService`/`PermissionService`, once the gRPC call lands there.

use std::sync::Arc;

use async_graphql_axum::{GraphQLRequest, GraphQLResponse};
use axum::extract::State;
use axum::http::{header, HeaderMap, HeaderValue, Method, StatusCode};
use axum::response::{IntoResponse, Response};
use axum::routing::{get, post};
use axum::Router;
use jsonwebtoken::DecodingKey;
use metap_crud::RecordBackend;
use metap_graphql::{with_request_data, Schema};
use metap_peripherals::decode_access_token;
use metap_permission::RequestContext;
use tower_http::cors::CorsLayer;

use crate::config::GatewayConfig;
use crate::schema_builder::BuiltSchema;

#[derive(Clone)]
struct GatewayState {
    schema: Arc<Schema>,
    backend: Arc<dyn RecordBackend>,
    decoding_key: Arc<DecodingKey>,
}

fn unauthorized(message: &str) -> Box<Response> {
    Box::new((StatusCode::UNAUTHORIZED, message.to_string()).into_response())
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
    let token = raw
        .strip_prefix("Bearer ")
        .ok_or_else(|| unauthorized("authorization header must be a Bearer token"))?;
    let claims = decode_access_token(token, decoding_key, 20).map_err(|_| unauthorized("invalid or expired token"))?;
    Ok(RequestContext {
        tenant_id: claims.tenant_id,
        user_id: Some(claims.sub),
        roles: None,
        function_id: claims.function_id,
        context_attributes: None,
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

    let cors = if config.cors_origins.is_empty() {
        CorsLayer::new()
    } else {
        let origins: Vec<HeaderValue> = config.cors_origins.iter().filter_map(|o| o.parse().ok()).collect();
        CorsLayer::new()
            .allow_origin(origins)
            .allow_credentials(true)
            .allow_methods([Method::GET, Method::POST])
            .allow_headers([header::AUTHORIZATION, header::CONTENT_TYPE, header::ACCEPT])
    };

    let mut app: Router<GatewayState> = Router::new()
        .route("/health", get(health))
        .route("/graphql", post(graphql_handler));

    // Same "unauthenticated static HTML, gate by env instead of by the crate itself" convention
    // `apps/jira-server/src/main.rs` already established for this exact router.
    if !config.is_production {
        app = app.merge(metap_graphql_http::playground_router::<GatewayState>("/graphql"));
    }

    let app = app
        .layer(cors)
        .layer(axum::middleware::from_fn(
            metap_http::security_headers::security_headers,
        ))
        .with_state(state);

    let addr = format!("{}:{}", config.host, config.port);
    let listener = tokio::net::TcpListener::bind(&addr).await?;
    tracing::info!(%addr, "listening");

    axum::serve(listener, app)
        .with_graceful_shutdown(shutdown_signal())
        .await?;
    Ok(())
}

async fn shutdown_signal() {
    tokio::signal::ctrl_c().await.ok();
    tracing::info!("shutdown signal received, exiting");
}
