//! Helmet-equivalent response headers — `packages/core/src/server/app.ts` (deleted, see
//! git history) registered `@fastify/helmet` with its library defaults; this middleware
//! reproduces that same default header set by hand, since no Rust crate ships an
//! axum-native "helmet" (Phase 8 Hardening scope). Applied globally in `metap_http::build_router`
//! so it also covers a downstream binary's static SPA fallback, not just `/api`/`/metadata` —
//! which is why the CSP below mirrors helmet's actual `'self'`-based default (safe for a
//! same-origin SPA) rather than a stricter `default-src 'none'` that would break it.
//!
//! Moved here from `metap-http` 2026-09-02 once `graphql-gateway` needed it too — it has zero
//! `AppState` dependency (a plain `fn(Request, Next) -> Response`), exactly the shape this
//! crate's other middleware (`rate_limit`/`trace`/`cors`/`request_id`) already has, and pulling
//! in all of `metap-http` (Postgres, 14 other `metap-*` crates) just for this one function was
//! real, unnecessary weight on a binary that has no Postgres pool of its own — see an
//! architecture audit, `../metap-docs/docs/audits/03-metap-core-architecture-audit.md` finding
//! #6, for how this was found. `metap-http` re-exports this module unchanged for its own
//! internal use and any existing external caller.

use axum::extract::Request;
use axum::http::header::CONTENT_SECURITY_POLICY;
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
    // `.entry(...).or_insert_with(...)`, not a blanket `.insert()` — a handler that needs a
    // genuinely different policy for its own response (e.g. `metap-graphql-http`'s
    // `playground_router`, which serves a third-party-script-loading GraphiQL page and can't
    // run under this API-shaped default) sets its own `content-security-policy` header before
    // this middleware runs; every other route (the overwhelming majority) never sets one, so
    // this default still applies to them exactly as before.
    headers
        .entry(CONTENT_SECURITY_POLICY)
        .or_insert_with(|| HeaderValue::from_static(CSP));
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
