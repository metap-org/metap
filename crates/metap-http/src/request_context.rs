//! Mirrors `packages/core/src/server/plugins/request-id.ts` (deleted, see git history):
//! echo/generate `x-request-id`/`x-trace-id` response headers on every request. Fastify's
//! version also attached both ids to a per-request child logger so every log line carried
//! them; axum has no equivalent per-request logger to hook here, so the closest match to
//! `error-handler.ts`'s `errorBody()` (which put both ids in every JSON error body) is done
//! centrally in this same middleware — buffering and re-serializing only 4xx/5xx bodies —
//! rather than threading a requestId/traceId parameter through the ~30
//! `service_error_response`/`internal_error_response` call sites across `routes/*.rs`.

use axum::body::{to_bytes, Body};
use axum::extract::Request;
use axum::http::header::CONTENT_LENGTH;
use axum::http::HeaderValue;
use axum::middleware::Next;
use axum::response::Response;
use uuid::Uuid;

/// Error bodies are small JSON objects; this is just a sanity ceiling, not a tuned limit.
const MAX_ERROR_BODY_BYTES: usize = 1024 * 1024;

fn is_valid_trace_id(value: &str) -> bool {
    !value.is_empty()
        && value.len() <= 128
        && value.bytes().all(|b| b.is_ascii_alphanumeric() || b == b'-')
}

pub async fn request_context(request: Request, next: Next) -> Response {
    let trace_id = request
        .headers()
        .get("x-trace-id")
        .and_then(|v| v.to_str().ok())
        .filter(|s| is_valid_trace_id(s))
        .map(str::to_string)
        .unwrap_or_else(|| Uuid::new_v4().to_string());
    let request_id = Uuid::new_v4().to_string();

    let response = next.run(request).await;
    let (mut parts, body) = response.into_parts();
    parts.headers.insert(
        "x-request-id",
        HeaderValue::from_str(&request_id).unwrap_or_else(|_| HeaderValue::from_static("invalid")),
    );
    parts.headers.insert(
        "x-trace-id",
        HeaderValue::from_str(&trace_id).unwrap_or_else(|_| HeaderValue::from_static("invalid")),
    );

    if !parts.status.is_client_error() && !parts.status.is_server_error() {
        return Response::from_parts(parts, body);
    }

    let bytes = match to_bytes(body, MAX_ERROR_BODY_BYTES).await {
        Ok(bytes) => bytes,
        Err(_) => return Response::from_parts(parts, Body::empty()),
    };
    let Ok(mut value) = serde_json::from_slice::<serde_json::Value>(&bytes) else {
        return Response::from_parts(parts, Body::from(bytes));
    };
    if let Some(error) = value.get_mut("error").and_then(|e| e.as_object_mut()) {
        error.insert("requestId".to_string(), serde_json::Value::String(request_id));
        error.insert("traceId".to_string(), serde_json::Value::String(trace_id));
    }
    let new_bytes = serde_json::to_vec(&value).unwrap_or_else(|_| bytes.to_vec());
    // The body length just changed; a stale Content-Length would make clients truncate or
    // hang waiting for bytes that aren't coming.
    parts.headers.remove(CONTENT_LENGTH);
    Response::from_parts(parts, Body::from(new_bytes))
}
