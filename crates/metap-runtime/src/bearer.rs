//! The `Authorization: Bearer <token>` prefix strip, factored out so it isn't reimplemented per
//! transport. Framework-agnostic on purpose — `metap-http/src/auth.rs` (axum), `graphql-gateway`
//! (axum, hand-rolled), and `metap-grpc/src/auth.rs` (tonic) each map the `None` case to their own
//! error type (`AuthError`, a boxed `Response`, `tonic::Status`); this only owns the string logic.

pub fn parse_bearer(header: &str) -> Option<&str> {
    let token = header.strip_prefix("Bearer ")?;
    let token = token.trim();
    if token.is_empty() {
        None
    } else {
        Some(token)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn extracts_token() {
        assert_eq!(parse_bearer("Bearer abc123"), Some("abc123"));
    }

    #[test]
    fn rejects_missing_prefix() {
        assert_eq!(parse_bearer("Basic abc123"), None);
    }

    #[test]
    fn rejects_empty_token() {
        assert_eq!(parse_bearer("Bearer "), None);
        assert_eq!(parse_bearer("Bearer"), None);
    }

    #[test]
    fn trims_incidental_whitespace() {
        assert_eq!(parse_bearer("Bearer  abc123  "), Some("abc123"));
    }
}
