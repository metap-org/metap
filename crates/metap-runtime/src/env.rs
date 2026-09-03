//! The `env::var(X).ok().and_then(|v| v.parse().ok()).unwrap_or(default)` idiom, found repeated
//! ~29 times in `metap-infra/src/config.rs` and independently reimplemented (6 times) in
//! `graphql-gateway/src/config.rs` instead of depending on `metap-infra::AppConfig` (that binary
//! has no Postgres/RabbitMQ config to load, so pulling in all of `AppConfig` isn't the fix — this
//! is the fix: the parsing idiom itself, not the config struct).

use std::env;
use std::str::FromStr;

pub fn env_or<T: FromStr>(name: &str, default: T) -> T {
    parse_or(env::var(name).ok().as_deref(), default)
}

/// `env_or`'s logic with the environment read lifted out.
///
/// Split so the tests below can exercise the parsing without mutating the process environment.
/// That is not a style preference: `setenv` is not thread-safe, cargo runs a crate's tests in
/// parallel threads of one binary, and a concurrent `setenv` can reallocate `environ` while
/// another thread is reading it — which is why Rust 2024 made `env::set_var` `unsafe` in the first
/// place. The previous version of these tests wrapped each call in `unsafe` with a SAFETY comment
/// claiming the test was single-threaded; it was not, and Semgrep's `unsafe-usage` rule flagging
/// all 16 of them is what surfaced it.
fn parse_or<T: FromStr>(raw: Option<&str>, default: T) -> T {
    raw.and_then(|v| v.parse().ok()).unwrap_or(default)
}

pub fn require_env(name: &str) -> anyhow::Result<String> {
    env::var(name).map_err(|_| anyhow::anyhow!("{name} is required"))
}

/// The `env::var(X).ok().filter(|s| !s.is_empty())` idiom — an optional string, where a set but
/// empty value counts as unset. Found repeated 23 times across `metap-infra`/`cron-scheduler`/
/// `graphql-gateway`/`dev-tools` during migration to this crate (2026-08-31) — a distinct idiom
/// from `env_or` (that one always has a default of the *target* type; this one stays a plain
/// optional string, since most callers layer their own fallback/validation on top).
pub fn optional(name: &str) -> Option<String> {
    non_empty(env::var(name).ok())
}

/// [`optional`]'s logic with the environment read lifted out — see [`parse_or`].
fn non_empty(raw: Option<String>) -> Option<String> {
    raw.filter(|s| !s.is_empty())
}

/// The `env::var(X).is_ok_and(|v| v == "true" || v == "1")` idiom — a boolean feature flag where
/// "set to something truthy" opts in, not merely "present" (a var set to `"false"` or `""` still
/// counts as off). Found duplicated as a private, byte-identical `env_flag_enabled` helper in
/// both `../metap-demo-crm` and `../metap-demo-jira`'s `main.rs` (`GRPC_ENABLED`/
/// `NOTIFICATION_WORKER_INLINE`/`OUTBOX_WORKER_INLINE`).
pub fn flag_enabled(name: &str) -> bool {
    is_truthy(env::var(name).ok().as_deref())
}

/// [`flag_enabled`]'s logic with the environment read lifted out — see [`parse_or`].
fn is_truthy(raw: Option<&str>) -> bool {
    matches!(raw, Some("true") | Some("1"))
}

#[cfg(test)]
mod tests {
    use super::*;

    // These test the extracted pure functions rather than the `env::var` wrappers, so nothing here
    // writes to the process environment. The "unset" cases still go through the public functions,
    // since reading an absent variable is safe and is the one behavior worth checking end to end.

    #[test]
    fn falls_back_to_default_when_unset() {
        assert_eq!(env_or::<u16>("METAP_CONTRIB_TEST_UNSET_VAR", 4000), 4000);
        assert_eq!(parse_or::<u16>(None, 4000), 4000);
    }

    #[test]
    fn parses_set_value() {
        assert_eq!(parse_or::<u16>(Some("8080"), 4000), 8080);
    }

    #[test]
    fn falls_back_on_unparseable_value() {
        assert_eq!(parse_or::<u16>(Some("not-a-number"), 4000), 4000);
    }

    #[test]
    fn require_env_errors_when_missing() {
        assert!(require_env("METAP_CONTRIB_TEST_REQUIRED_UNSET").is_err());
    }

    #[test]
    fn optional_none_when_unset() {
        assert_eq!(optional("METAP_CONTRIB_TEST_OPTIONAL_UNSET"), None);
        assert_eq!(non_empty(None), None);
    }

    #[test]
    fn optional_none_when_empty() {
        assert_eq!(non_empty(Some(String::new())), None);
    }

    #[test]
    fn optional_some_when_set() {
        assert_eq!(non_empty(Some("value".to_string())), Some("value".to_string()));
    }

    #[test]
    fn flag_enabled_false_when_unset() {
        assert!(!flag_enabled("METAP_CONTRIB_TEST_FLAG_UNSET"));
        assert!(!is_truthy(None));
    }

    #[test]
    fn flag_enabled_true_for_true_or_1() {
        assert!(is_truthy(Some("true")));
        assert!(is_truthy(Some("1")));
    }

    #[test]
    fn flag_enabled_false_for_other_values() {
        assert!(!is_truthy(Some("false")));
        assert!(!is_truthy(Some("")));
        // Truthiness is exact-match, not "any non-empty value" — a var set to `TRUE` or `yes` is
        // deliberately off, which is the behavior the public function has always had.
        assert!(!is_truthy(Some("TRUE")));
        assert!(!is_truthy(Some("yes")));
    }
}
