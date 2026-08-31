//! `reqwest::Client::new()` has no request timeout — a hung upstream hangs the caller forever.
//! Found live in `docs/features/08-metap-contrib-common-crate.md`'s survey:
//! `graphql-gateway/src/schema_builder.rs` and `metap-jwks/src/client.rs` both built a bare
//! `reqwest::Client::new()` this way; only `cron-scheduler` had set a timeout (30s), by hand.
//! `default_client()` is that same 30s default, in one place.

use std::time::Duration;

pub const DEFAULT_TIMEOUT: Duration = Duration::from_secs(30);

pub fn build(timeout: Duration) -> reqwest::Result<reqwest::Client> {
    reqwest::Client::builder().timeout(timeout).build()
}

/// `reqwest::Client::builder().timeout(DEFAULT_TIMEOUT)` never fails to build in practice (no TLS
/// config, no invalid header) — panicking here is the same trade `reqwest::Client::new()` itself
/// makes internally.
pub fn default_client() -> reqwest::Client {
    build(DEFAULT_TIMEOUT).expect("default reqwest client config is always valid")
}

/// Attaches the current request's W3C `traceparent` (`crate::trace_context::current()`) to an
/// outbound call, if this is running inside one (`crate::trace_context::scope`) — a no-op
/// otherwise, e.g. a boot-time or background call with no incoming request to propagate from.
/// Opt-in, not automatic: none of this crate's own consumers today (`graphql-gateway`'s upstream
/// metadata fetch, `metap-jwks`'s JWKS refresh, `cron-scheduler`'s callback client) run inside a
/// request scope, so there's nothing yet to wire this into by default — call it on your own
/// `reqwest::RequestBuilder` once you have a caller that does.
pub fn attach_trace_context(builder: reqwest::RequestBuilder) -> reqwest::RequestBuilder {
    match crate::trace_context::current() {
        Some(ctx) => builder.header("traceparent", ctx.to_traceparent_header()),
        None => builder,
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn default_client_builds() {
        let _ = default_client();
    }

    #[test]
    fn build_respects_custom_timeout() {
        let client = build(Duration::from_secs(5));
        assert!(client.is_ok());
    }

    #[test]
    fn attach_trace_context_is_a_no_op_outside_a_scope() {
        let builder = default_client().get("http://example.invalid");
        let request = attach_trace_context(builder).build().unwrap();
        assert!(request.headers().get("traceparent").is_none());
    }

    #[tokio::test]
    async fn attach_trace_context_sets_the_header_inside_a_scope() {
        let ctx = crate::trace_context::from_headers(&Default::default());
        let trace_id = ctx.trace_id.clone();
        crate::trace_context::scope(ctx, async {
            let builder = default_client().get("http://example.invalid");
            let request = attach_trace_context(builder).build().unwrap();
            let header = request
                .headers()
                .get("traceparent")
                .unwrap()
                .to_str()
                .unwrap()
                .to_string();
            assert!(header.contains(&trace_id));
        })
        .await;
    }
}
