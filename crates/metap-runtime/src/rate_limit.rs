//! Per-peer-IP rate limiting, moved out of `metap-http::build_router` so a from-scratch custom
//! router (e.g. `graphql-gateway`, which had none at all) can opt into the same production-grade
//! default instead of missing rate limiting entirely.
//!
//! Approximates the old `@fastify/rate-limit` default (`max: 300, timeWindow: "1 minute"`) with
//! `tower_governor`'s token-bucket model instead of a fixed window: a burst capacity of
//! `burst_size`, replenishing one token every `per_millisecond` ms. Keyed by peer IP
//! (`PeerIpKeyExtractor`, the default) rather than `SmartIpKeyExtractor`'s `X-Forwarded-For`/
//! `X-Real-IP` — those headers are attacker-spoofable unless a trusted reverse proxy strips/
//! overwrites them first, and no production deployment topology says one does yet. A caller
//! using this layer must serve via `into_make_service_with_connect_info::<SocketAddr>()`, not
//! plain `into_make_service()` — `PeerIpKeyExtractor` reads the connection's peer address from
//! that extension, which only that serving method populates.

use axum::body::Body;
use axum::http::{header, HeaderValue};
use governor::middleware::NoOpMiddleware;
use tower_governor::governor::GovernorConfigBuilder;
use tower_governor::key_extractor::PeerIpKeyExtractor;
use tower_governor::{GovernorError, GovernorLayer};

pub fn build(per_millisecond: u64, burst_size: u32) -> GovernorLayer<PeerIpKeyExtractor, NoOpMiddleware, Body> {
    let governor_conf = GovernorConfigBuilder::default()
        .per_millisecond(per_millisecond)
        .burst_size(burst_size)
        .finish()
        .expect("static rate-limit config is always valid");
    GovernorLayer::new(governor_conf).error_handler(|err| match err {
        GovernorError::TooManyRequests { wait_time, .. } => {
            let mut response = crate::http_error::service_error_response(
                429,
                "too_many_requests",
                Some(&format!("Too many requests. Retry after {wait_time}s.")),
                None,
            );
            if let Ok(value) = HeaderValue::from_str(&wait_time.to_string()) {
                response.headers_mut().insert(header::RETRY_AFTER, value);
            }
            response
        }
        _ => crate::http_error::internal_error_response(anyhow::anyhow!("rate limiter: {err}")),
    })
}
