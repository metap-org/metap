//! The `{"error":{"code":...,"message":...}}` axum error-response shape, moved here from
//! `metap-http/src/error.rs` (2026-08-31) so a service with no `metap-http`/`AppState`/Postgres
//! dependency at all — a custom router in a from-scratch project like `../metap-demo-waf`,
//! `graphql-gateway`'s own standalone axum app — can still return the same response shape every
//! `metap-http`-based service does, without pulling in that crate's much heavier dependency tree.
//! `metap-http::error` re-exports [`service_error_response`]/[`internal_error_response`] from
//! here unchanged, so no existing caller's import path breaks; it keeps its own
//! `router_unavailable_response` locally since that one needs `metap_control::RouterError`, a
//! business-specific type this crate must not depend on.

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

#[cfg(test)]
mod tests {
    use axum::body::to_bytes;

    use super::*;

    #[tokio::test]
    async fn service_error_response_uses_default_message() {
        let response = service_error_response(404, "entity_not_found", None, None);
        assert_eq!(response.status(), StatusCode::NOT_FOUND);
        let body = to_bytes(response.into_body(), usize::MAX).await.unwrap();
        let json: serde_json::Value = serde_json::from_slice(&body).unwrap();
        assert_eq!(json["error"]["code"], "entity_not_found");
        assert_eq!(json["error"]["message"], "Entity not found.");
    }

    #[tokio::test]
    async fn service_error_response_uses_custom_message() {
        let response = service_error_response(400, "validation_failed", Some("bad field"), None);
        let body = to_bytes(response.into_body(), usize::MAX).await.unwrap();
        let json: serde_json::Value = serde_json::from_slice(&body).unwrap();
        assert_eq!(json["error"]["message"], "bad field");
    }

    #[tokio::test]
    async fn service_error_response_includes_field_errors() {
        let mut field_errors = HashMap::new();
        field_errors.insert("name".to_string(), vec!["required".to_string()]);
        let response = service_error_response(400, "validation_failed", None, Some(field_errors));
        let body = to_bytes(response.into_body(), usize::MAX).await.unwrap();
        let json: serde_json::Value = serde_json::from_slice(&body).unwrap();
        assert_eq!(json["error"]["fieldErrors"]["name"][0], "required");
    }

    #[tokio::test]
    async fn internal_error_response_hides_details() {
        let response = internal_error_response(anyhow::anyhow!("db connection failed"));
        assert_eq!(response.status(), StatusCode::INTERNAL_SERVER_ERROR);
        let body = to_bytes(response.into_body(), usize::MAX).await.unwrap();
        let json: serde_json::Value = serde_json::from_slice(&body).unwrap();
        assert_eq!(json["error"]["code"], "internal_error");
        assert!(!json["error"]["message"].as_str().unwrap().contains("db connection"));
    }
}
