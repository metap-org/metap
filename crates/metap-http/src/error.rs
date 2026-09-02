//! Mirrors the shape (not the full richness — no `requestId`/`traceId` here, a deliberate
//! simplification; both are injected centrally into every error response, see
//! `crates/metap-runtime/src/request_id.rs`, moved there from this crate 2026-08-31) of
//! `packages/core/src/server/error-handler.ts`'s error body and
//! `SERVICE_ERROR_MESSAGES` default-message table.
//!
//! [`service_error_response`]/[`internal_error_response`] moved to `metap-runtime::http_error`
//! (2026-08-31) — re-exported here unchanged so no existing caller's import path breaks — so a
//! service with no `AppState`/Postgres dependency (a from-scratch custom router, e.g. a future
//! `../metap-demo-waf` admin API) can get the same response shape without depending on this
//! whole crate. [`router_unavailable_response`] stays here: it needs `metap_control::RouterError`,
//! a business-specific type `metap-runtime` must not depend on.

use axum::response::Response;

pub use metap_runtime::http_error::{internal_error_response, service_error_response};

/// Maps a `Router::begin` failure to an HTTP response — same mapping
/// `metap-crud::crud_service`'s `router_unavailable` uses for record CRUD, reused here now that
/// `POST /auth/login`, role lookup, and `/admin/users`/`/admin/policies` also open a
/// `Router`-scoped transaction (`docs/roadmap.md` Phase 16 gap, closed 2026-08-20). Falls back
/// to `internal_error_response` for anything that isn't a `RouterError` (shouldn't happen —
/// `Router::begin` only ever fails with one) or is `InvalidSchemaName` (a corrupted
/// `control.tenants` row, not a caller-facing condition).
pub fn router_unavailable_response(err: anyhow::Error) -> Response {
    match err.downcast_ref::<metap_control::RouterError>() {
        Some(metap_control::RouterError::TenantSuspended | metap_control::RouterError::TenantExpired) => {
            service_error_response(403, "tenant_unavailable", None, None)
        }
        Some(metap_control::RouterError::TenantMigrating | metap_control::RouterError::TenantProvisioning) => {
            service_error_response(503, "tenant_unavailable", None, None)
        }
        Some(metap_control::RouterError::TenantDeleted) => service_error_response(404, "tenant_not_found", None, None),
        _ => internal_error_response(err),
    }
}
