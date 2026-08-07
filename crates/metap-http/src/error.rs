//! Mirrors the shape (not the full richness — no `requestId`/`traceId`; see this module's
//! note in `docs/rust-core-viability.md`'s Migration Order) of
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
    let message = message.map(str::to_string).unwrap_or_else(|| default_message(error).to_string());
    let mut body = serde_json::json!({ "error": { "code": error, "message": message } });
    if let Some(field_errors) = field_errors {
        body["error"]["fieldErrors"] = serde_json::to_value(field_errors).unwrap_or_default();
    }
    let code = StatusCode::from_u16(status).unwrap_or(StatusCode::INTERNAL_SERVER_ERROR);
    (code, Json(body)).into_response()
}

pub fn internal_error_response(err: anyhow::Error) -> Response {
    eprintln!("[metap-http] internal error: {err:#}");
    (
        StatusCode::INTERNAL_SERVER_ERROR,
        Json(serde_json::json!({
            "error": { "code": "internal_error", "message": "Internal server error." }
        })),
    )
        .into_response()
}
