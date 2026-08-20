//! Mirrors the shape (not the full richness — no `requestId`/`traceId` here, a deliberate
//! simplification; both are injected centrally into every error response, see
//! `crates/metap-http/src/request_id.rs`) of
//! `packages/core/src/server/error-handler.ts`'s error body and
//! `SERVICE_ERROR_MESSAGES` default-message table.

use std::collections::HashMap;

use axum::http::StatusCode;
use axum::response::{IntoResponse, Response};
use axum::Json;

fn default_message(error: &str) -> &'static str {
    match error {
        "entity_not_found" => "Entity not found.",
        "forbidden" => "You do not have permission to perform this action.",
        "validation_failed" => "Request validation failed.",
        "insert_failed" => "Failed to create the record.",
        "record_not_found" => "Record not found.",
        "version_conflict" => "The record was modified by someone else. Reload and try again.",
        "no_workflow" => "This entity has no workflow.",
        "invalid_transition" => "This transition is not valid from the record's current state.",
        "guard_failed" => "This transition is not allowed.",
        "invalid_cursor" => "The pagination cursor is invalid.",
        _ => "Request failed.",
    }
}

pub fn service_error_response(
    status: u16,
    error: &str,
    message: Option<&str>,
    field_errors: Option<HashMap<String, Vec<String>>>,
) -> Response {
    let message = message
        .map(str::to_string)
        .unwrap_or_else(|| default_message(error).to_string());
    let mut body = serde_json::json!({ "error": { "code": error, "message": message } });
    if let Some(field_errors) = field_errors {
        body["error"]["fieldErrors"] = serde_json::to_value(field_errors).unwrap_or_default();
    }
    let code = StatusCode::from_u16(status).unwrap_or(StatusCode::INTERNAL_SERVER_ERROR);
    (code, Json(body)).into_response()
}

pub fn internal_error_response(err: anyhow::Error) -> Response {
    tracing::error!(error = %format!("{err:#}"), "internal error");
    (
        StatusCode::INTERNAL_SERVER_ERROR,
        Json(serde_json::json!({
            "error": { "code": "internal_error", "message": "Internal server error." }
        })),
    )
        .into_response()
}

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
        _ => internal_error_response(err),
    }
}
