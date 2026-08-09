//! Generates the request/trace id pair once, before anything else runs, and stashes it in the
//! request extensions — the single source both `TraceLayer` (below, in `lib.rs`) and
//! `request_context` (response headers + error body) read from, instead of each minting or
//! parsing its own copy. Must be the outermost layer in `build_router` so every other layer's
//! work (and any `tracing` event logged from deep inside a handler — `metap-crud`,
//! `metap-permission`, etc.) falls inside the span `TraceLayer` builds from these ids.

use axum::extract::Request;
use axum::middleware::Next;
use axum::response::Response;
use uuid::Uuid;

#[derive(Clone)]
pub struct RequestIds {
    pub request_id: String,
    pub trace_id: String,
}

fn is_valid_trace_id(value: &str) -> bool {
    !value.is_empty()
        && value.len() <= 128
        && value.bytes().all(|b| b.is_ascii_alphanumeric() || b == b'-')
}

pub async fn generate_request_ids(mut request: Request, next: Next) -> Response {
    let trace_id = request
        .headers()
        .get("x-trace-id")
        .and_then(|v| v.to_str().ok())
        .filter(|s| is_valid_trace_id(s))
        .map(str::to_string)
        .unwrap_or_else(|| Uuid::new_v4().to_string());
    let request_id = Uuid::new_v4().to_string();

    request.extensions_mut().insert(RequestIds { request_id, trace_id });
    next.run(request).await
}
