//! The `env::var(X).ok().and_then(|v| v.parse().ok()).unwrap_or(default)` idiom, found repeated
//! ~29 times in `metap-infra/src/config.rs` and independently reimplemented (6 times) in
//! `graphql-gateway/src/config.rs` instead of depending on `metap-infra::AppConfig` (that binary
//! has no Postgres/RabbitMQ config to load, so pulling in all of `AppConfig` isn't the fix — this
//! is the fix: the parsing idiom itself, not the config struct).

use std::env;
use std::str::FromStr;

pub fn env_or<T: FromStr>(name: &str, default: T) -> T {
    env::var(name).ok().and_then(|v| v.parse().ok()).unwrap_or(default)
}

pub fn require_env(name: &str) -> anyhow::Result<String> {
    env::var(name).map_err(|_| anyhow::anyhow!("{name} is required"))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn falls_back_to_default_when_unset() {
        assert_eq!(env_or::<u16>("METAP_CONTRIB_TEST_UNSET_VAR", 4000), 4000);
    }

    #[test]
    fn parses_set_value() {
        // SAFETY: single-threaded test, no other code reads this var concurrently.
        unsafe { env::set_var("METAP_CONTRIB_TEST_PORT", "8080") };
        assert_eq!(env_or::<u16>("METAP_CONTRIB_TEST_PORT", 4000), 8080);
        unsafe { env::remove_var("METAP_CONTRIB_TEST_PORT") };
    }

    #[test]
    fn falls_back_on_unparseable_value() {
        unsafe { env::set_var("METAP_CONTRIB_TEST_BAD_PORT", "not-a-number") };
        assert_eq!(env_or::<u16>("METAP_CONTRIB_TEST_BAD_PORT", 4000), 4000);
        unsafe { env::remove_var("METAP_CONTRIB_TEST_BAD_PORT") };
    }

    #[test]
    fn require_env_errors_when_missing() {
        assert!(require_env("METAP_CONTRIB_TEST_REQUIRED_UNSET").is_err());
    }
}
