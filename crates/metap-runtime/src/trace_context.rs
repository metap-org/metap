//! W3C Trace Context (`traceparent` header, <https://www.w3.org/TR/trace-context/>) — the chunk
//! of plumbing a service needs to interoperate with a service mesh sidecar (Istio/Envoy,
//! Linkerd) without adopting the full OpenTelemetry SDK: the sidecar does the actual span
//! creation/export, this crate only needs to read the incoming `traceparent` (or start a new
//! trace if absent), keep it ambient for the duration of the request, and re-attach it to any
//! outbound call this service makes so the mesh can stitch the whole call chain together.
//!
//! Deliberately additive, not a replacement for `request_id`'s existing `x-trace-id`/`RequestIds`
//! — that mechanism keeps working unchanged; this is a separate, W3C-standard mechanism for
//! mesh interop specifically.
//!
//! Ambient via `tokio::task_local!` rather than threaded through every function call — axum
//! serves each request in its own task, so [`scope`] wrapping the outermost middleware's
//! `next.run(request)` makes [`current`] available for the whole request, including calls made
//! deep inside a handler (`metap-grpc::client::GrpcBackend::signed_request` reads it). Does
//! **not** cross a `tokio::spawn` boundary — a handler that spawns its own child task loses the
//! ambient context in that child (task-locals are task-scoped, not thread-scoped); no handler in
//! this codebase does that today.

use axum::http::{HeaderMap, HeaderValue};
use uuid::Uuid;

#[derive(Debug, Clone)]
pub struct TraceContext {
    /// 32 hex chars, fixed for the entire call chain.
    pub trace_id: String,
    /// The caller's own span id, if this request arrived with a valid `traceparent`.
    pub parent_span_id: Option<String>,
    /// Freshly generated for this hop — becomes `parent_span_id` for whatever this service
    /// calls next.
    pub span_id: String,
    pub sampled: bool,
}

fn new_trace_id() -> String {
    Uuid::new_v4().simple().to_string()
}

fn new_span_id() -> String {
    Uuid::new_v4().simple().to_string()[..16].to_string()
}

/// `version-traceid-parentid-flags`, e.g. `00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01`
/// — only version `00` is defined today, an all-zero trace-id/parent-id is explicitly invalid
/// per spec (rejected here rather than accepted as "no propagation").
fn parse_traceparent(header: &str) -> Option<(String, String, bool)> {
    let mut parts = header.split('-');
    let version = parts.next()?;
    let trace_id = parts.next()?;
    let parent_id = parts.next()?;
    let flags = parts.next()?;
    if parts.next().is_some() {
        return None;
    }
    if version.len() != 2 || trace_id.len() != 32 || parent_id.len() != 16 || flags.len() != 2 {
        return None;
    }
    let is_hex = |s: &str| s.bytes().all(|b| b.is_ascii_hexdigit());
    if !is_hex(version) || !is_hex(trace_id) || !is_hex(parent_id) || !is_hex(flags) {
        return None;
    }
    if trace_id.bytes().all(|b| b == b'0') || parent_id.bytes().all(|b| b == b'0') {
        return None;
    }
    let flags_byte = u8::from_str_radix(flags, 16).ok()?;
    Some((trace_id.to_string(), parent_id.to_string(), flags_byte & 0x01 != 0))
}

/// Parses the incoming `traceparent` header if present and well-formed; otherwise starts a new
/// root trace (this service is the entry point — e.g. called directly, outside any mesh, or the
/// mesh's own ingress hasn't set one).
pub fn from_headers(headers: &HeaderMap) -> TraceContext {
    match headers
        .get("traceparent")
        .and_then(|v| v.to_str().ok())
        .and_then(parse_traceparent)
    {
        Some((trace_id, parent_span_id, sampled)) => TraceContext {
            trace_id,
            parent_span_id: Some(parent_span_id),
            span_id: new_span_id(),
            sampled,
        },
        None => TraceContext {
            trace_id: new_trace_id(),
            parent_span_id: None,
            span_id: new_span_id(),
            sampled: true,
        },
    }
}

impl TraceContext {
    /// Builds the `traceparent` value to send on an outbound call — this hop's own `span_id`
    /// becomes the `parent_span_id` the callee will see.
    pub fn to_traceparent_header(&self) -> HeaderValue {
        let flags = if self.sampled { "01" } else { "00" };
        let value = format!("00-{}-{}-{flags}", self.trace_id, self.span_id);
        HeaderValue::from_str(&value).expect("well-formed traceparent is always a valid header value")
    }
}

tokio::task_local! {
    static CURRENT: TraceContext;
}

/// `None` outside a [`scope`] (e.g. a boot-time or background call with no incoming request to
/// propagate from) — callers must treat that as "nothing to attach", not an error.
pub fn current() -> Option<TraceContext> {
    CURRENT.try_with(Clone::clone).ok()
}

pub async fn scope<F: std::future::Future>(ctx: TraceContext, f: F) -> F::Output {
    CURRENT.scope(ctx, f).await
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parses_a_well_formed_traceparent() {
        let mut headers = HeaderMap::new();
        headers.insert(
            "traceparent",
            HeaderValue::from_static("00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"),
        );
        let ctx = from_headers(&headers);
        assert_eq!(ctx.trace_id, "4bf92f3577b34da6a3ce929d0e0e4736");
        assert_eq!(ctx.parent_span_id.as_deref(), Some("00f067aa0ba902b7"));
        assert!(ctx.sampled);
        assert_eq!(ctx.span_id.len(), 16);
    }

    #[test]
    fn starts_a_new_root_trace_when_header_is_absent() {
        let ctx = from_headers(&HeaderMap::new());
        assert_eq!(ctx.trace_id.len(), 32);
        assert!(ctx.parent_span_id.is_none());
    }

    #[test]
    fn rejects_malformed_headers_and_starts_a_new_trace_instead() {
        let mut headers = HeaderMap::new();
        headers.insert("traceparent", HeaderValue::from_static("not-a-valid-traceparent"));
        let ctx = from_headers(&headers);
        assert!(ctx.parent_span_id.is_none());
    }

    #[test]
    fn rejects_all_zero_trace_id() {
        let mut headers = HeaderMap::new();
        headers.insert(
            "traceparent",
            HeaderValue::from_static("00-00000000000000000000000000000000-00f067aa0ba902b7-01"),
        );
        let ctx = from_headers(&headers);
        assert!(ctx.parent_span_id.is_none());
    }

    #[test]
    fn to_traceparent_header_round_trips() {
        let ctx = TraceContext {
            trace_id: "4bf92f3577b34da6a3ce929d0e0e4736".to_string(),
            parent_span_id: None,
            span_id: "00f067aa0ba902b7".to_string(),
            sampled: true,
        };
        let header = ctx.to_traceparent_header();
        let (trace_id, parent_id, sampled) = parse_traceparent(header.to_str().unwrap()).unwrap();
        assert_eq!(trace_id, ctx.trace_id);
        assert_eq!(parent_id, ctx.span_id);
        assert!(sampled);
    }

    #[tokio::test]
    async fn current_is_none_outside_a_scope() {
        assert!(current().is_none());
    }

    #[tokio::test]
    async fn current_is_some_inside_a_scope() {
        let ctx = from_headers(&HeaderMap::new());
        let trace_id = ctx.trace_id.clone();
        scope(ctx, async {
            assert_eq!(current().unwrap().trace_id, trace_id);
        })
        .await;
        assert!(current().is_none());
    }
}
