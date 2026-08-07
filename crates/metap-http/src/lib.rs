//! Mirrors `packages/core/src/server/app.ts`'s `buildApp` — minus `helmet`/rate-limiting
//! (Phase 8 Hardening scope, not this step's "thin wiring over `CrudService`" goal, see
//! `docs/rust-core-viability.md`'s Migration Order step 8 note) and minus the admin routes
//! (`RoleAssignmentService`'s write side, policy admin CRUD, `IndexReconciler`,
//! `MetadataDriftService` — Migration Order step 9, Peripherals). `AuthContext` is
//! extracted per-handler (see `auth.rs`) rather than as a route-group-scoped hook, axum's
//! idiomatic equivalent of Fastify's `onRequest` hook scoped to a route group — same
//! effect (every protected handler requires and validates a bearer token), different
//! mechanism.

pub mod auth;
pub mod error;
pub mod routes;
pub mod state;

use axum::http::{header, HeaderValue, Method};
use axum::Router;
use tower_http::cors::CorsLayer;

pub use auth::AuthContext;
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

    Router::new()
        .merge(routes::health::router())
        .merge(routes::metadata::public_router())
        .merge(routes::metadata::protected_router())
        .merge(routes::records::router())
        .layer(cors)
        .with_state(state)
}
