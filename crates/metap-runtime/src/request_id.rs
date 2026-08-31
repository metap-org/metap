//! Generates the request/trace id pair once, before anything else runs, and stashes it in the
//! request extensions — the single source both `trace::build`'s `TraceLayer` and
//! `request_context::request_context` (response headers + error body) read from, instead of
//! each minting or parsing its own copy. Must be the outermost layer in a router's middleware
//! stack so every other layer's work (and any `tracing` event logged from deep inside a
//! handler) falls inside the span `trace::build`'s layer builds from these ids.
//!
//! Also the entry point for `trace_context`'s W3C `traceparent` propagation (mesh interop,
//! separate from and additive to the `x-trace-id`/`RequestIds` mechanism below): parses the
//! incoming `traceparent` if present, and makes it ambient for the rest of the request via
//! `trace_context::scope` so anything called deeper (`metap-grpc`'s outbound calls) can read it
//! back via `trace_context::current()`.

use axum::extract::Request;
use axum::middleware::Next;
use axum::response::Response;
use uuid::Uuid;

use crate::trace_context;

#[derive(Clone)]
pub struct RequestIds {
    pub request_id: String,
    pub trace_id: String,
}

fn is_valid_opaque_id(value: &str) -> bool {
    !value.is_empty() && value.len() <= 128 && value.bytes().all(|b| b.is_ascii_alphanumeric() || b == b'-')
}

pub async fn generate_request_ids(mut request: Request, next: Next) -> Response {
    let trace_id = request
        .headers()
        .get("x-trace-id")
        .and_then(|v| v.to_str().ok())
        .filter(|s| is_valid_opaque_id(s))
        .map(str::to_string)
        .unwrap_or_else(|| Uuid::new_v4().to_string());

    // A mesh ingress (Istio's Envoy sidecar) sets `x-request-id` before this service ever sees
    // the request, and expects it preserved end-to-end for its own access-log correlation —
    // generating a fresh one here would silently break that. Only mint a new one when the
    // header is genuinely absent (e.g. called directly, outside any mesh).
    let request_id = request
        .headers()
        .get("x-request-id")
        .and_then(|v| v.to_str().ok())
        .filter(|s| is_valid_opaque_id(s))
        .map(str::to_string)
        .unwrap_or_else(|| Uuid::new_v4().to_string());

    let trace_ctx = trace_context::from_headers(request.headers());

    request.extensions_mut().insert(RequestIds { request_id, trace_id });
    trace_context::scope(trace_ctx, next.run(request)).await
}
