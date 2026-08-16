//! Mirrors `packages/core/src/server/plugins/request-id.ts` (deleted, see git history):
//! echo/generate `x-request-id`/`x-trace-id` response headers on every request. Fastify's
//! version also attached both ids to a per-request child logger so every log line carried
//! them; the Rust equivalent is `request_id::generate_request_ids` (outermost layer, see
//! `lib.rs`) stashing the same ids in request extensions for `TraceLayer` to fold into its
//! span, so every `tracing` event logged anywhere during the request carries them without
//! this module's help. What's left here is `error-handler.ts`'s `errorBody()` behavior (both
//! ids in every JSON error body) — done centrally in this middleware, buffering and
//! re-serializing only 4xx/5xx bodies, rather than threading a requestId/traceId parameter
//! through the ~30 `service_error_response`/`internal_error_response` call sites across
//! `routes/*.rs`.

use axum::body::{to_bytes, Body};
use axum::extract::Request;
use axum::http::header::CONTENT_LENGTH;
use axum::http::HeaderValue;
use axum::middleware::Next;
use axum::response::Response;

use crate::request_id::RequestIds;

/// Error bodies are small JSON objects; this is just a sanity ceiling, not a tuned limit.
const MAX_ERROR_BODY_BYTES: usize = 1024 * 1024;

pub async fn request_context(request: Request, next: Next) -> Response {
    // Always present — `generate_request_ids` runs outside this layer in `build_router`, so
    // by construction every request reaching here already has one.
    let RequestIds { request_id, trace_id } =
        request
            .extensions()
            .get::<RequestIds>()
            .cloned()
            .unwrap_or_else(|| RequestIds {
                request_id: uuid::Uuid::new_v4().to_string(),
                trace_id: uuid::Uuid::new_v4().to_string(),
            });

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
