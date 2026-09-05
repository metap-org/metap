//! Cookie/CSRF constants and the pure double-submit check every transport that accepts this
//! platform's cookie-based session needs to agree on — moved out of `metap-http::cookies` (which
//! re-exports these, unchanged for existing callers) once `crates/metap-graphql-gateway` needed the
//! same check too (opt-in cookie fallback alongside its existing Bearer-only auth, for a
//! same-origin deployment like `../metap-demo-waf`'s — see that crate's `server.rs`). Lives here,
//! not moved wholesale into `metap-http`, for the same "avoid the heavier dependency" reason
//! `metap_runtime::http_error`'s own doc comment gives: a from-scratch binary with no
//! Postgres/`CrudService` (this crate's whole reason to exist, per its own module doc) shouldn't
//! need to pull in all of `metap-http` just for these 2 pure functions.
//!
//! Deliberately does NOT include `metap_http::cookies::session_cookies`/`clear_session_cookies`
//! (issuing/clearing the cookie pair) — those need `axum_extra`/`time` and only ever run in the
//! one process that actually mints sessions (`POST /auth/login`, `crates/metap-http`); a verifier
//! that never issues a cookie has no reason to depend on those crates too.

use axum::http::Method;

pub const SESSION_COOKIE_NAME: &str = "metap_session";
pub const CSRF_COOKIE_NAME: &str = "metap_csrf";
/// Request header a browser client must echo the [`CSRF_COOKIE_NAME`] cookie's value into for any
/// state-changing (non-GET/HEAD/OPTIONS) cookie-authenticated request — see this module's own doc
/// comment for why. Lowercase: `HeaderName` comparison is case-insensitive regardless, lowercase
/// is just this crate's own convention for header name constants.
pub const CSRF_HEADER_NAME: &str = "x-csrf-token";

/// Whether a cookie-authenticated request of this method must carry a matching
/// [`CSRF_HEADER_NAME`] header. Safe methods are exempt because they must not change state — the
/// standard double-submit rule. **Not sufficient on its own for `POST /graphql`** — every GraphQL
/// operation rides the same HTTP method regardless of whether it's a query or a mutation, so a
/// cookie-authenticated caller there must always send the CSRF header; a caller of this function
/// gates on that itself rather than relying on this predicate to tell queries and mutations apart.
pub fn requires_csrf_check(method: &Method) -> bool {
    !matches!(*method, Method::GET | Method::HEAD | Method::OPTIONS)
}

/// The double-submit comparison itself: the [`CSRF_COOKIE_NAME`] cookie's value must be present,
/// non-empty, and equal to the [`CSRF_HEADER_NAME`] header's.
///
/// Both-absent and both-empty are rejected explicitly. Without the emptiness guard, a request that
/// somehow presents an empty cookie *and* an empty header would compare equal and pass — a state
/// no legitimate client produces, so treating it as a match could only ever help a caller that
/// shouldn't be there.
pub fn csrf_matches(cookie_value: Option<&str>, header_value: Option<&str>) -> bool {
    match (cookie_value, header_value) {
        (Some(cookie), Some(header)) => !cookie.is_empty() && cookie == header,
        _ => false,
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn only_safe_methods_skip_the_csrf_check() {
        for exempt in [Method::GET, Method::HEAD, Method::OPTIONS] {
            assert!(!requires_csrf_check(&exempt), "{exempt} must be exempt");
        }
        for guarded in [Method::POST, Method::PUT, Method::PATCH, Method::DELETE] {
            assert!(requires_csrf_check(&guarded), "{guarded} must be CSRF-checked");
        }
    }

    #[test]
    fn csrf_matches_only_when_both_are_present_and_equal() {
        assert!(csrf_matches(Some("abc"), Some("abc")));
        assert!(!csrf_matches(Some("abc"), Some("different")));
        assert!(!csrf_matches(Some("abc"), None), "header missing must not pass");
        assert!(!csrf_matches(None, Some("abc")), "cookie missing must not pass");
        assert!(!csrf_matches(None, None), "both missing must not pass");
    }

    #[test]
    fn empty_values_never_match_each_other() {
        assert!(!csrf_matches(Some(""), Some("")));
        assert!(!csrf_matches(Some(""), None));
    }
}
