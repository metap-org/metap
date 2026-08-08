//! Mirrors `packages/core/src/server/app.ts`'s `buildApp`. `AuthContext`/`AdminContext` are
//! extracted per-handler (see `auth.rs`) rather than as a route-group-scoped hook, axum's
//! idiomatic equivalent of Fastify's `onRequest` hook scoped to a route group — same
//! effect (every protected handler requires and validates a bearer token, admin routes
//! additionally require the `admin` role), different mechanism. `routes::admin` is the HTTP
//! surface for role assignment (`metap_peripherals`) and policy CRUD/explain
//! (`PermissionService`) — both existed only as tested functions until this route module was
//! added (see `docs/architectures/11-risks.md`).
//!
//! Phase 8 Hardening (`docs/roadmap.md`): `security_headers` (helmet-equivalent),
//! `request_context` (requestId/traceId), and per-peer-IP rate limiting are all applied
//! here, globally, rather than per-route — same "can't be forgotten on a future route"
//! reasoning as `AdminContext`. Rate limiting needs the caller's `axum::serve` to know the
//! peer address: **any binary using `build_router` must serve via
//! `into_make_service_with_connect_info::<SocketAddr>()`**, not plain `into_make_service()`
//! — see `apps/crm-server/src/main.rs` and `metap-http/tests/http_server.rs` for the two
//! call sites this repo has. Still deferred: a secret manager, CSP/sanitizer/file-scanning
//! (none apply — this is a JSON API with no HTML rendering or file upload yet), non-root
//! Docker image, CI checks, load tests, backup/restore drill.

pub mod auth;
pub mod error;
pub mod request_context;
pub mod routes;
pub mod security_headers;
pub mod state;

use axum::http::{header, HeaderValue, Method};
use axum::middleware;
use axum::Router;
use tower_governor::governor::GovernorConfigBuilder;
use tower_governor::{GovernorError, GovernorLayer};
use tower_http::cors::CorsLayer;

pub use auth::{AdminContext, AuthContext};
pub use state::AppState;

pub fn build_router(state: AppState, cors_origins: &[String]) -> Router {
    let cors = if cors_origins.is_empty() {
        CorsLayer::new()
    } else {
        let origins: Vec<HeaderValue> =
            cors_origins.iter().filter_map(|o| o.parse().ok()).collect();
        // `allow_credentials(true)` cannot be combined with a wildcard `Any` for
        // origin/headers — the CORS spec forbids it, and tower-http enforces this at
        // runtime (a hard panic, not a type error), so both origins and headers must be an
        // explicit list. Caught by actually running the server, not by any unit/e2e test —
        // the e2e test in `metap-http/tests/` always passed an empty `cors_origins`, which
        // takes the `CorsLayer::new()` branch below and never exercised this combination.
        CorsLayer::new()
            .allow_origin(origins)
            .allow_credentials(true)
            .allow_methods([Method::GET, Method::POST, Method::PATCH, Method::DELETE])
            .allow_headers([header::AUTHORIZATION, header::CONTENT_TYPE, header::ACCEPT])
    };

    // Approximates the old `@fastify/rate-limit` default (`max: 300, timeWindow: "1
    // minute"`, see git history on the now-deleted `packages/core/src/server/app.ts`) with
    // governor's token-bucket model instead of a fixed window: a burst capacity of 300,
    // replenishing 5 tokens/sec (= 300/min sustained). Keyed by peer IP
    // (`PeerIpKeyExtractor`, the default) rather than `SmartIpKeyExtractor`'s
    // `X-Forwarded-For`/`X-Real-IP` — those headers are attacker-spoofable unless a trusted
    // reverse proxy strips/overwrites them first, and no production deployment topology is
    // documented yet (`docs/architectures/11-risks.md`) to say one does. Revisit once one
    // exists.
    let governor_conf = GovernorConfigBuilder::default()
        .per_millisecond(200)
        .burst_size(300)
        .finish()
        .expect("static rate-limit config is always valid");
    let rate_limit = GovernorLayer::new(governor_conf).error_handler(|err| match err {
        GovernorError::TooManyRequests { wait_time, .. } => {
            let mut response = error::service_error_response(
                429,
                "too_many_requests",
                Some(&format!("Too many requests. Retry after {wait_time}s.")),
                None,
            );
            if let Ok(value) = HeaderValue::from_str(&wait_time.to_string()) {
                response.headers_mut().insert(header::RETRY_AFTER, value);
            }
            response
        }
        _ => error::internal_error_response(anyhow::anyhow!("rate limiter: {err}")),
    });

    Router::new()
        .merge(routes::health::router())
        .merge(routes::metadata::public_router())
        .merge(routes::metadata::protected_router())
        .merge(routes::records::router())
        .merge(routes::admin::router())
        .layer(cors)
        .layer(rate_limit)
        .layer(middleware::from_fn(request_context::request_context))
        .layer(middleware::from_fn(security_headers::security_headers))
        .with_state(state)
}
