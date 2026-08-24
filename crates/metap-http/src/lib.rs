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
pub mod cache;
pub mod error;
pub mod metrics;
pub mod request_context;
pub mod request_id;
pub mod routes;
pub mod security_headers;
pub mod state;

use axum::extract::Request;
use axum::http::{header, HeaderValue, Method};
use axum::middleware;
use axum::Router;
use tower_governor::governor::GovernorConfigBuilder;
use tower_governor::{GovernorError, GovernorLayer};
use tower_http::classify::ServerErrorsFailureClass;
use tower_http::cors::CorsLayer;
use tower_http::trace::TraceLayer;
use tracing::Span;

use request_id::RequestIds;

pub use auth::{AdminContext, AuthContext, PlatformAdminContext};
pub use state::AppState;

/// `extra_routes` is the extension point for optional platform capabilities that are not
/// core — `metap-lowcode-http`'s admin API is the first (only) one today, merged in by
/// `apps/crm-server/src/main.rs` as `metap_lowcode_http::router()`, never by this crate
/// itself: `metap-http` has zero dependency on `metap-lowcode`/`metap-lowcode-http`, so a
/// binary that doesn't want the low-code control plane can pass `Router::new()` here and
/// never compile that crate in. Merged *before* the layers below so `extra_routes` gets the
/// exact same CORS/rate-limit/tracing/security-header treatment as every core route — a
/// caller merging it in *after* `build_router` returns would bypass all of that.
pub fn build_router(state: AppState, cors_origins: &[String], extra_routes: Router<AppState>) -> Router {
    let cors = if cors_origins.is_empty() {
        CorsLayer::new()
    } else {
        let origins: Vec<HeaderValue> = cors_origins.iter().filter_map(|o| o.parse().ok()).collect();
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

    // One span per request, carrying the same request_id/trace_id `request_context` puts in
    // the response — so a `tracing` event logged anywhere downstream (a permission denial in
    // `metap-permission`, a validation failure in `metap-crud`, ...) is automatically
    // correlated with both the client-visible ids and this access-log line, with no id
    // threaded through any of those crates' function signatures.
    let trace = TraceLayer::new_for_http()
        .make_span_with(|request: &Request| {
            let ids = request.extensions().get::<RequestIds>();
            tracing::info_span!(
                "http_request",
                method = %request.method(),
                path = %request.uri().path(),
                request_id = ids.map(|i| i.request_id.as_str()).unwrap_or("unknown"),
                trace_id = ids.map(|i| i.trace_id.as_str()).unwrap_or("unknown"),
                status = tracing::field::Empty,
                latency_ms = tracing::field::Empty,
            )
        })
        .on_response(
            |response: &axum::response::Response, latency: std::time::Duration, span: &Span| {
                span.record("status", response.status().as_u16());
                span.record("latency_ms", latency.as_millis() as u64);
                tracing::event!(parent: span, tracing::Level::INFO, "request completed");
            },
        )
        .on_failure(
            |error: ServerErrorsFailureClass, latency: std::time::Duration, span: &Span| {
                span.record("latency_ms", latency.as_millis() as u64);
                tracing::event!(parent: span, tracing::Level::ERROR, %error, "request failed");
            },
        );

    Router::new()
        .merge(routes::health::router())
        .merge(routes::metrics::router())
        .merge(routes::metadata::public_router())
        .merge(routes::metadata::protected_router())
        .merge(routes::records::router())
        .merge(routes::attachments::router())
        .merge(routes::admin::router())
        .merge(routes::auth::router())
        .merge(routes::cron::router())
        .merge(routes::preferences::router())
        .merge(extra_routes)
        .layer(cors)
        .layer(rate_limit)
        .layer(middleware::from_fn(request_context::request_context))
        .layer(middleware::from_fn(security_headers::security_headers))
        .layer(trace)
        // Records per-route request count/duration/in-flight (`docs/local-benchmarking.md`) —
        // must wrap route dispatch (inside `request_id`/outside nothing route-specific) so it
        // sees the actual matched route and final response status, same positioning as `trace`.
        .layer(metrics::metric_layer())
        // Outermost: every layer/handler below runs with `RequestIds` already in the request
        // extensions, and — via `trace` above — inside the span built from them.
        .layer(middleware::from_fn(request_id::generate_request_ids))
        .with_state(state)
}
