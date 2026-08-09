//! Helmet-equivalent response headers — `packages/core/src/server/app.ts` (deleted, see
//! git history) registered `@fastify/helmet` with its library defaults; this middleware
//! reproduces that same default header set by hand, since no Rust crate ships an
//! axum-native "helmet" (Phase 8 Hardening scope, see `docs/roadmap.md` and this crate's
//! `lib.rs` doc comment). Applied globally in `build_router` so it also covers
//! `apps/crm-server`'s static SPA fallback, not just `/api`/`/metadata` — which is why the
//! CSP below mirrors helmet's actual `'self'`-based default (safe for a same-origin SPA)
//! rather than a stricter `default-src 'none'` that would break it.

use axum::extract::Request;
use axum::http::HeaderValue;
use axum::middleware::Next;
use axum::response::Response;

const CSP: &str = "default-src 'self'; base-uri 'self'; font-src 'self' https: data:; \
                    form-action 'self'; frame-ancestors 'self'; img-src 'self' data:; \
                    object-src 'none'; script-src 'self'; script-src-attr 'none'; \
                    style-src 'self' https: 'unsafe-inline'; upgrade-insecure-requests";

pub async fn security_headers(request: Request, next: Next) -> Response {
    let mut response = next.run(request).await;
    let headers = response.headers_mut();
    headers.insert("content-security-policy", HeaderValue::from_static(CSP));
    headers.insert("cross-origin-opener-policy", HeaderValue::from_static("same-origin"));
    headers.insert("cross-origin-resource-policy", HeaderValue::from_static("same-origin"));
    headers.insert("origin-agent-cluster", HeaderValue::from_static("?1"));
    headers.insert("referrer-policy", HeaderValue::from_static("no-referrer"));
    headers.insert(
        "strict-transport-security",
        HeaderValue::from_static("max-age=15552000; includeSubDomains"),
    );
    headers.insert("x-content-type-options", HeaderValue::from_static("nosniff"));
    headers.insert("x-dns-prefetch-control", HeaderValue::from_static("off"));
    headers.insert("x-download-options", HeaderValue::from_static("noopen"));
    headers.insert("x-frame-options", HeaderValue::from_static("SAMEORIGIN"));
    headers.insert("x-permitted-cross-domain-policies", HeaderValue::from_static("none"));
    headers.insert("x-xss-protection", HeaderValue::from_static("0"));
    response
}
