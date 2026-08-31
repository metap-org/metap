//! One `tracing` span per request, moved out of `metap-http::build_router` so any router built
//! on top of this crate gets the same request/trace-id-correlated access log for free. Carries
//! the same `request_id`/`trace_id` `request_id::generate_request_ids` puts in the request
//! extensions — one `tracing` event logged anywhere downstream during the request (deep inside
//! a handler, a permission denial, a validation failure) is automatically correlated with both
//! the client-visible ids and this access-log line, with no id threaded through any call chain.
//! Requires `request_id::generate_request_ids` to run as an outer layer first.

use std::convert::Infallible;
use std::time::Duration;

use axum::extract::Request;
use axum::response::{IntoResponse, Response};
use axum::routing::Route;
use tower::{Layer, Service};
use tower_http::classify::ServerErrorsFailureClass;
use tower_http::trace::TraceLayer;
use tracing::Span;

use crate::request_id::RequestIds;
use crate::trace_context;

pub fn build() -> impl Layer<
    Route,
    Service: Service<
        Request,
        Response: IntoResponse + 'static,
        Error: Into<Infallible> + 'static,
        Future: Send + 'static,
    > + Clone
                 + Send
                 + Sync
                 + 'static,
> + Clone
       + Send
       + Sync
       + 'static {
    TraceLayer::new_for_http()
        .make_span_with(|request: &Request| {
            let ids = request.extensions().get::<RequestIds>();
            // `trace_id`/`span_id` come from `trace_context` (W3C `traceparent` — the mesh's own
            // trace id, or a freshly started one if this hop is the entry point), not the
            // separate `x-trace-id`/`RequestIds` mechanism (still carried as `legacy_trace_id`,
            // still in response headers/error bodies via `request_context`, just not what a
            // mesh's tracing backend correlates on).
            let trace_ctx = trace_context::current();
            tracing::info_span!(
                "http_request",
                method = %request.method(),
                path = %request.uri().path(),
                request_id = ids.map(|i| i.request_id.as_str()).unwrap_or("unknown"),
                legacy_trace_id = ids.map(|i| i.trace_id.as_str()).unwrap_or("unknown"),
                trace_id = trace_ctx.as_ref().map(|c| c.trace_id.as_str()).unwrap_or("unknown"),
                span_id = trace_ctx.as_ref().map(|c| c.span_id.as_str()).unwrap_or("unknown"),
                status = tracing::field::Empty,
                latency_ms = tracing::field::Empty,
            )
        })
        .on_response(|response: &Response, latency: Duration, span: &Span| {
            span.record("status", response.status().as_u16());
            span.record("latency_ms", latency.as_millis() as u64);
            tracing::event!(parent: span, tracing::Level::INFO, "request completed");
        })
        .on_failure(|error: ServerErrorsFailureClass, latency: Duration, span: &Span| {
            span.record("latency_ms", latency.as_millis() as u64);
            tracing::event!(parent: span, tracing::Level::ERROR, %error, "request failed");
        })
}
