//! Mirrors `packages/core/src/server/app.ts`'s `buildApp`. `AuthContext`/`AdminContext` are
//! extracted per-handler (see `auth.rs`) rather than as a route-group-scoped hook, axum's
//! idiomatic equivalent of Fastify's `onRequest` hook scoped to a route group — same
//! effect (every protected handler requires and validates a bearer token, admin routes
//! additionally require the `admin` role), different mechanism. `routes::admin` is the HTTP
//! surface for role assignment (`metap_peripherals`) and policy CRUD/explain
//! (`PermissionService`) — both existed only as tested functions until this route module was
//! added (see `docs/architectures/11-risks.md`).
//!
//! Phase 8 Hardening (`docs/roadmap.md`): `security_headers` (helmet-equivalent, moved to
//! `metap-runtime` 2026-09-02, re-exported here unchanged — see that module's doc comment for
//! why), `request_context` (requestId/traceId), and per-peer-IP rate limiting are all applied
//! here, globally, rather than per-route — same "can't be forgotten on a future route"
//! reasoning as `AdminContext`. Rate limiting needs the caller's `axum::serve` to know the
//! peer address: **any binary using `build_router` must serve via
//! `into_make_service_with_connect_info::<SocketAddr>()`**, not plain `into_make_service()`
//! — see `../metap-demo-crm/src/main.rs` and `metap-http/tests/http_server.rs` for the two
//! call sites this repo has. Still deferred: a secret manager, CSP/sanitizer/file-scanning
//! (none apply — this is a JSON API with no HTML rendering or file upload yet), non-root
//! Docker image, CI checks, load tests, backup/restore drill.

pub mod auth;
pub mod cache;
pub mod cookies;
pub mod error;
pub mod metrics;
pub mod openapi_paths;
pub mod routes;
pub mod state;

pub use metap_runtime::security_headers;

use axum::http::{header, Method};
use axum::middleware;
use axum::Router;

pub use auth::{AdminContext, AuthContext, PlatformAdminContext};
pub use state::AppState;

// `request_id`/`request_context` middleware and the rate-limit/tracing-span layer builders
// used in `build_router` below all moved to `metap-runtime` (2026-08-31,
// `docs/features/08-metap-runtime-common-crate.md`) — pure `axum`/`tower` plumbing with zero
// dependency on this crate's own `AppState`/business logic, so any router built on
// `metap-runtime` (not just this crate) can get the same production-grade defaults. Referenced
// via `metap_runtime::request_context`/`metap_runtime::request_id` below, not re-exported —
// no external caller referenced `metap_http::request_id`/`metap_http::request_context` before
// this move (verified by grep).

/// Which of `build_router`'s optional route groups actually get mounted — every field defaults
/// to `true` (`RouteGroups::all()`, what `build_router` itself still passes unconditionally), so
/// an existing caller is completely unaffected. Only 4 groups are toggleable at all, chosen from
/// a real cross-app usage survey (2026-09-15) rather than guessed: `attachments`/`cron`/
/// `dashboards`/`tenant_config` are each genuinely unused by at least one real downstream app
/// today (`../metap-demo-waf`'s 3 services use none of `attachments`/`dashboards` — confirmed by
/// grepping its web app and backend for any caller; `../metap-demo-jira` uses none of
/// `tenant_config`), while every other group (`health`/`metrics`/`metadata`/`records`/`users`/
/// `auth`/`preferences`/`workflow_events`/`audit_events`/`admin`/`platform_config`) stays
/// unconditionally mounted — either genuinely core (auth, metadata, the CRUD surface itself) or
/// cheap enough, and surprising enough to 404 on, that no real survey found a case worth the
/// toggle. Mounting a group nobody calls isn't a correctness bug (an unused route is just unused
/// surface area, same as the doc comment on `AppState`'s unused fields notes elsewhere) — this
/// exists so a deployment that wants a smaller attack surface / a leaner `/metadata/openapi.json`
/// can actually get one, not because leaving them all on was ever broken.
#[derive(Debug, Clone, Copy)]
pub struct RouteGroups {
    pub attachments: bool,
    pub cron: bool,
    pub dashboards: bool,
    pub tenant_config: bool,
}

impl RouteGroups {
    pub fn all() -> Self {
        Self {
            attachments: true,
            cron: true,
            dashboards: true,
            tenant_config: true,
        }
    }
}

impl Default for RouteGroups {
    fn default() -> Self {
        Self::all()
    }
}

/// `extra_routes` is the extension point for optional platform capabilities that are not
/// core — `metap-lowcode-http`'s admin API is the first (only) one today, merged in by
/// `../metap-demo-crm/src/main.rs` as `metap_lowcode_http::router()`, never by this crate
/// itself: `metap-http` has zero dependency on `metap-lowcode`/`metap-lowcode-http`, so a
/// binary that doesn't want the low-code control plane can pass `Router::new()` here and
/// never compile that crate in. Merged *before* the layers below so `extra_routes` gets the
/// exact same CORS/rate-limit/tracing/security-header treatment as every core route — a
/// caller merging it in *after* `build_router` returns would bypass all of that.
///
/// Mounts every route group (`RouteGroups::all()`) — the existing, unchanged entry point every
/// caller in this codebase already uses. A binary that wants to trim `attachments`/`cron`/
/// `dashboards`/`tenant_config` calls [`build_router_with_groups`] instead; see that function
/// and [`RouteGroups`]'s own doc comments.
pub fn build_router(state: AppState, cors_origins: &[String], extra_routes: Router<AppState>) -> Router {
    build_router_with_groups(state, cors_origins, extra_routes, RouteGroups::all())
}

/// Same as [`build_router`], with `groups` controlling which of the 4 toggleable route groups
/// (see [`RouteGroups`]'s own doc comment) actually get mounted. `build_router` itself is a thin
/// wrapper calling this with `RouteGroups::all()` — the two can never drift.
pub fn build_router_with_groups(
    state: AppState,
    cors_origins: &[String],
    extra_routes: Router<AppState>,
    groups: RouteGroups,
) -> Router {
    // `metap_runtime::cors::build`'s doc comment has the `allow_credentials(true)` + wildcard
    // `Any` panic-risk this guards against — same code path `graphql-gateway` uses, only the
    // allowed methods/headers below are specific to this crate's full REST surface (PATCH/DELETE
    // included, unlike `graphql-gateway`'s GraphQL-only GET/POST).
    let cors = metap_runtime::cors::build(
        cors_origins,
        &[Method::GET, Method::POST, Method::PATCH, Method::DELETE],
        &[header::AUTHORIZATION, header::CONTENT_TYPE, header::ACCEPT],
    );

    // `metap_runtime::rate_limit::build`'s doc comment has the full reasoning (token-bucket
    // model, peer-IP keying, why this crate's `into_make_service_with_connect_info` requirement
    // exists) — 200ms/300 burst approximates the old `@fastify/rate-limit` default (`max: 300,
    // timeWindow: "1 minute"`, see git history on the now-deleted
    // `packages/core/src/server/app.ts`).
    // Both numbers are `PlatformGlobal` config keys now (`docs/features/18-config-tiers-db-backed.md`),
    // defaulting to exactly the 200/300 they were hard-coded as. Read once here rather than per
    // request: `GovernorLayer` is built once and owns its own token buckets, so a change through
    // `PUT /platform/config` reaches this only on the next `build_router` — noted in that route's
    // response rather than pretended otherwise.
    let config = state.config.current();
    let rate_limit = metap_runtime::rate_limit::build(
        config.get_u64(metap_config::keys::HTTP_RATE_LIMIT_PER_MS),
        config.get_u64(metap_config::keys::HTTP_RATE_LIMIT_BURST) as u32,
    );

    // `metap_runtime::trace::build`'s doc comment has the full reasoning — one span per
    // request, correlated with the same request_id/trace_id `request_context` puts in the
    // response.
    let trace = metap_runtime::trace::build();

    let mut router = Router::new()
        .merge(routes::health::router())
        .merge(routes::metrics::router())
        .merge(routes::metadata::public_router())
        .merge(routes::metadata::protected_router())
        .merge(routes::records::router())
        .merge(routes::users::router())
        .merge(routes::workflow_events::router())
        .merge(routes::audit_events::router())
        .merge(routes::admin::router())
        .merge(routes::auth::router())
        .merge(routes::platform_config::router())
        .merge(routes::preferences::router());
    if groups.attachments {
        router = router.merge(routes::attachments::router());
    }
    if groups.cron {
        router = router.merge(routes::cron::router());
    }
    if groups.dashboards {
        router = router.merge(routes::dashboards::router());
    }
    if groups.tenant_config {
        router = router.merge(routes::tenant_config::router());
    }

    router
        .merge(extra_routes)
        .layer(cors)
        .layer(rate_limit)
        .layer(middleware::from_fn(metap_runtime::request_context::request_context))
        .layer(middleware::from_fn(security_headers::security_headers))
        .layer(trace)
        // Records per-route request count/duration/in-flight (`docs/local-benchmarking.md`) —
        // must wrap route dispatch (inside `request_id`/outside nothing route-specific) so it
        // sees the actual matched route and final response status, same positioning as `trace`.
        .layer(metrics::metric_layer())
        // Outermost: every layer/handler below runs with `RequestIds` already in the request
        // extensions, and — via `trace` above — inside the span built from them.
        .layer(middleware::from_fn(metap_runtime::request_id::generate_request_ids))
        .with_state(state)
}
