//! Per-peer-IP rate limiting for this crate's own gRPC listener (audit 04 finding A#3 — this
//! transport had no rate limiting at all, unlike `metap-http`/`graphql-gateway`, both of which go
//! through `metap_runtime::rate_limit::build`). That function can't be reused directly here: it
//! returns a `tower_governor` `GovernorLayer` hardcoded to `axum::body::Body` (`tower_governor`'s
//! "axum" feature bakes an axum `Response` conversion into the type), while a tonic service
//! speaks `tonic::body::Body` instead — pulling in `tower_governor` a second time with an
//! incompatible feature set would be more machinery than this warrants. This is a small,
//! standalone `tower_layer::Layer`/`tower_service::Service` pair built directly on `governor`
//! (the crate `tower_governor` itself wraps around) instead — same token-bucket shape and
//! burst/refill parameters as the HTTP version, only the request/response types differ.

use std::net::{IpAddr, SocketAddr};
use std::num::NonZeroU32;
use std::pin::Pin;
use std::sync::Arc;
use std::task::{Context, Poll};
use std::time::Duration;

use governor::{DefaultKeyedRateLimiter, Quota};
use http::{Request, Response};
use tonic::body::Body;
use tonic::transport::server::TcpConnectInfo;
use tonic::Status;
use tower_layer::Layer;
use tower_service::Service;

/// Same token-bucket shape as `metap_runtime::rate_limit::build`: a burst capacity of
/// `burst_size`, replenishing one token every `per_millisecond` ms. Keyed by the real TCP peer
/// address (`TcpConnectInfo`, populated by `tonic::transport::Server` for every accepted
/// connection) — unlike the HTTP layer's `PeerIpKeyExtractor` there is no `X-Forwarded-For`-style
/// spoofing concern to weigh here, since this only ever sees the actual socket peer, never a
/// caller-supplied header.
#[derive(Clone)]
pub struct RateLimitLayer {
    limiter: Arc<DefaultKeyedRateLimiter<IpAddr>>,
}

impl RateLimitLayer {
    pub fn new(per_millisecond: u64, burst_size: u32) -> Self {
        let quota = Quota::with_period(Duration::from_millis(per_millisecond.max(1)))
            .expect("per_millisecond is always > 0 after the .max(1) above")
            .allow_burst(NonZeroU32::new(burst_size.max(1)).expect("burst_size is always > 0 after the .max(1) above"));
        Self {
            limiter: Arc::new(DefaultKeyedRateLimiter::keyed(quota)),
        }
    }
}

impl<S> Layer<S> for RateLimitLayer {
    type Service = RateLimitService<S>;

    fn layer(&self, inner: S) -> Self::Service {
        RateLimitService {
            inner,
            limiter: self.limiter.clone(),
        }
    }
}

#[derive(Clone)]
pub struct RateLimitService<S> {
    inner: S,
    limiter: Arc<DefaultKeyedRateLimiter<IpAddr>>,
}

impl<S, ReqBody> Service<Request<ReqBody>> for RateLimitService<S>
where
    S: Service<Request<ReqBody>, Response = Response<Body>> + Clone + Send + 'static,
    S::Future: Send + 'static,
    ReqBody: Send + 'static,
{
    type Response = Response<Body>;
    type Error = S::Error;
    type Future = Pin<Box<dyn std::future::Future<Output = Result<Self::Response, Self::Error>> + Send>>;

    fn poll_ready(&mut self, cx: &mut Context<'_>) -> Poll<Result<(), Self::Error>> {
        self.inner.poll_ready(cx)
    }

    fn call(&mut self, req: Request<ReqBody>) -> Self::Future {
        let peer_ip = req
            .extensions()
            .get::<TcpConnectInfo>()
            .and_then(TcpConnectInfo::remote_addr)
            .map(|addr: SocketAddr| addr.ip());

        // No peer IP on this request (a non-TCP transport, e.g. a unix socket) — fail open rather
        // than block every request; this crate only ever serves over TCP today, so this branch is
        // not currently reachable, just not assumed away.
        if let Some(ip) = peer_ip {
            if self.limiter.check_key(&ip).is_err() {
                let response = Status::resource_exhausted("Too many requests.").into_http::<Body>();
                return Box::pin(async move { Ok(response) });
            }
        }

        let mut inner = self.inner.clone();
        Box::pin(async move { inner.call(req).await })
    }
}

#[cfg(test)]
mod tests {
    use std::convert::Infallible;
    use std::net::{IpAddr, Ipv4Addr, SocketAddr};

    use tonic::Code;

    use super::*;

    #[derive(Clone)]
    struct Echo;

    impl Service<Request<()>> for Echo {
        type Response = Response<Body>;
        type Error = Infallible;
        type Future = std::future::Ready<Result<Self::Response, Self::Error>>;

        fn poll_ready(&mut self, _cx: &mut Context<'_>) -> Poll<Result<(), Self::Error>> {
            Poll::Ready(Ok(()))
        }

        fn call(&mut self, _req: Request<()>) -> Self::Future {
            std::future::ready(Ok(Response::new(Body::empty())))
        }
    }

    fn request_from(ip: IpAddr) -> Request<()> {
        let mut req = Request::new(());
        req.extensions_mut().insert(TcpConnectInfo {
            local_addr: None,
            remote_addr: Some(SocketAddr::new(ip, 12345)),
        });
        req
    }

    #[tokio::test]
    async fn allows_calls_within_the_burst_then_rejects_the_next_one() {
        let mut svc = RateLimitLayer::new(3_600_000, 3).layer(Echo);
        let ip = IpAddr::V4(Ipv4Addr::new(127, 0, 0, 1));

        for _ in 0..3 {
            let response = svc.call(request_from(ip)).await.unwrap();
            assert!(
                response.extensions().get::<Status>().is_none(),
                "burst calls should reach Echo"
            );
        }

        let rejected = svc.call(request_from(ip)).await.unwrap();
        let status = rejected
            .extensions()
            .get::<Status>()
            .expect("the 4th call within the burst window must be rejected");
        assert_eq!(status.code(), Code::ResourceExhausted);
    }

    #[tokio::test]
    async fn tracks_separate_peers_independently() {
        let mut svc = RateLimitLayer::new(3_600_000, 1).layer(Echo);
        let a = IpAddr::V4(Ipv4Addr::new(10, 0, 0, 1));
        let b = IpAddr::V4(Ipv4Addr::new(10, 0, 0, 2));

        assert!(svc
            .call(request_from(a))
            .await
            .unwrap()
            .extensions()
            .get::<Status>()
            .is_none());
        // `a` is now exhausted, but `b` has never been charged — must not share `a`'s bucket.
        assert!(svc
            .call(request_from(b))
            .await
            .unwrap()
            .extensions()
            .get::<Status>()
            .is_none());
        let rejected = svc.call(request_from(a)).await.unwrap();
        assert_eq!(
            rejected.extensions().get::<Status>().unwrap().code(),
            Code::ResourceExhausted
        );
    }

    #[tokio::test]
    async fn fails_open_when_no_peer_ip_is_present() {
        let mut svc = RateLimitLayer::new(3_600_000, 1).layer(Echo);
        // No `TcpConnectInfo` extension at all — e.g. a non-TCP transport.
        for _ in 0..5 {
            let response = svc.call(Request::new(())).await.unwrap();
            assert!(response.extensions().get::<Status>().is_none());
        }
    }
}
