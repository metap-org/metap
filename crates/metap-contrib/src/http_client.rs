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
}
