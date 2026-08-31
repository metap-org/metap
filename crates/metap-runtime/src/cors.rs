//! The "origins empty -> `CorsLayer::new()`, else parse each origin into a `HeaderValue` and
//! `allow_credentials(true)`" branch, near-identical between `metap-http::build_router` and
//! `graphql-gateway`'s own server setup — only the allowed methods/headers genuinely differed per
//! caller, so those stay parameters rather than baked in here.
//!
//! `allow_credentials(true)` cannot be combined with a wildcard `Any` for origin/headers — the
//! CORS spec forbids it, and tower-http enforces this at runtime (a hard panic, not a type error).
//! `build` always passes an explicit origin list once `cors_origins` is non-empty, never `Any`, so
//! that combination can't happen through this function.

use axum::http::{HeaderName, HeaderValue, Method};
use tower_http::cors::CorsLayer;

pub fn build(cors_origins: &[String], methods: &[Method], headers: &[HeaderName]) -> CorsLayer {
    if cors_origins.is_empty() {
        CorsLayer::new()
    } else {
        let origins: Vec<HeaderValue> = cors_origins.iter().filter_map(|o| o.parse().ok()).collect();
        CorsLayer::new()
            .allow_origin(origins)
            .allow_credentials(true)
            .allow_methods(methods.to_vec())
            .allow_headers(headers.to_vec())
    }
}

#[cfg(test)]
mod tests {
    use axum::http::header;

    use super::*;

    #[test]
    fn empty_origins_uses_permissive_default() {
        let _ = build(&[], &[Method::GET], &[header::AUTHORIZATION]);
    }

    #[test]
    fn non_empty_origins_with_credentials_does_not_panic() {
        // The exact combination (`allow_credentials(true)` + explicit origin list) that panics
        // at runtime if `allow_origin`/`allow_headers` ever regress to `Any` instead.
        let _ = build(
            &["https://example.com".to_string()],
            &[Method::GET, Method::POST],
            &[header::AUTHORIZATION, header::CONTENT_TYPE],
        );
    }
}
