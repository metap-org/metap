//! The typed config-key registry — the single place that decides **which tier a key belongs to**.
//!
//! This is the load-bearing invariant of the whole feature (`docs/features/18-config-tiers-db-backed.md`):
//! a key's tier is a property **of the key**, declared here in Rust, never of the caller asking for
//! it and never a column in the table. The reason is concrete rather than stylistic — the SSRF
//! guard `cron-scheduler` gained for audit 04 A#1 is only worth anything because its host allowlist
//! is *operator*-controlled. The moment a tenant admin (or even a platform admin) can flip
//! `CRON_WEBHOOK_ALLOW_PRIVATE_TARGETS` through a convenient config API, that fix is undone with no
//! exploit required at all. A tier stored as data would itself need protecting by something, and
//! that something would be this list anyway.
//!
//! So: an `Operator` key is readable by nobody's API and writable by nobody's API. It exists in
//! this registry **precisely so that the write surface can refuse it by name** — see
//! [`ConfigLevel::Operator`]'s own note.

use serde_json::Value;

/// Who may write a key — and, for `Operator`, that nobody may.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum ConfigLevel {
    /// Deployment-level, env-var only. **No API writes it**, not `/admin/config` (tenant admin) and
    /// not `/platform/config` (platform admin) either — a platform admin is not the same person as
    /// the operator holding shell/env access, and security-critical settings answer to the latter.
    ///
    /// Keys declared at this level are deliberately still listed in [`REGISTRY`] even though their
    /// value never comes from the database: listing them is what lets the write surface reject them
    /// by name with a 403 instead of silently accepting an unknown key, and it makes the boundary
    /// testable (`operator_keys_are_rejected_by_every_write_surface`) rather than implicit in the
    /// absence of an entry.
    Operator,
    /// Fleet-wide default, written by a `platform_admin` through `PUT /platform/config`.
    PlatformGlobal,
    /// Per-tenant, written by that tenant's own admin. Reserved by the registry now; the
    /// `tenant_configs` table and `/admin/config` surface land in slice 2 of the feature brief, so
    /// no key declares this level yet.
    Tenant,
}

/// One declared key: its tier, its default, and how to validate a proposed value.
///
/// `default` is a real value rather than an `Option` — a key with no row in the table must read
/// back as the same number the code used to hard-code, never as `null`. That is what keeps this
/// whole layer additive: a deployment that never touches `/platform/config` behaves exactly as it
/// did before the table existed.
pub struct ConfigKeyDef {
    pub key: &'static str,
    pub level: ConfigLevel,
    pub default: fn() -> Value,
    /// `Err(reason)` rejects the write. Reason text goes back to the caller, so write it for the
    /// person typing the value, not for a log.
    pub validate: fn(&Value) -> Result<(), String>,
}

/// Rejects anything that is not a positive integer within `[min, max]`.
///
/// Bounds are not decoration: every key below feeds something that fails badly at zero (a
/// `burst_size` of 0 rejects every request; a session TTL of 0 mints already-expired tokens), and
/// an absurd upper value silently disables the guardrail the setting exists to provide.
fn positive_int_in(value: &Value, min: u64, max: u64) -> Result<(), String> {
    match value.as_u64() {
        Some(n) if n >= min && n <= max => Ok(()),
        Some(n) => Err(format!("must be between {min} and {max}, got {n}")),
        None => Err("must be a positive integer".to_string()),
    }
}

pub const GRAPHQL_MAX_DEPTH: &str = "graphql.maxDepth";
pub const GRAPHQL_MAX_COMPLEXITY: &str = "graphql.maxComplexity";
pub const HTTP_RATE_LIMIT_PER_MS: &str = "http.rateLimitPerMillisecond";
pub const HTTP_RATE_LIMIT_BURST: &str = "http.rateLimitBurst";
pub const AUTH_SESSION_TTL_SECONDS: &str = "auth.sessionTtlSeconds";
pub const CRON_WEBHOOK_ALLOW_PRIVATE_TARGETS: &str = "cron.webhookAllowPrivateTargets";
pub const CRON_WEBHOOK_ALLOWED_HOSTS: &str = "cron.webhookAllowedHosts";
pub const CORS_ORIGINS: &str = "http.corsOrigins";

/// Every key the platform knows. Adding one here is the *only* way to make it settable.
pub const REGISTRY: &[ConfigKeyDef] = &[
    // --- PlatformGlobal: the values that were hard-coded in code until this feature ---
    ConfigKeyDef {
        key: GRAPHQL_MAX_DEPTH,
        level: ConfigLevel::PlatformGlobal,
        default: || Value::from(10),
        // Below 3, ordinary nested queries this platform generates for Reference fields stop
        // working at all; above 50 the limit no longer bounds anything a hostile query would do.
        validate: |v| positive_int_in(v, 3, 50),
    },
    ConfigKeyDef {
        key: GRAPHQL_MAX_COMPLEXITY,
        level: ConfigLevel::PlatformGlobal,
        default: || Value::from(1000),
        validate: |v| positive_int_in(v, 10, 1_000_000),
    },
    ConfigKeyDef {
        key: HTTP_RATE_LIMIT_PER_MS,
        level: ConfigLevel::PlatformGlobal,
        default: || Value::from(200),
        validate: |v| positive_int_in(v, 1, 60_000),
    },
    ConfigKeyDef {
        key: HTTP_RATE_LIMIT_BURST,
        level: ConfigLevel::PlatformGlobal,
        default: || Value::from(300),
        validate: |v| positive_int_in(v, 1, 100_000),
    },
    ConfigKeyDef {
        key: AUTH_SESSION_TTL_SECONDS,
        level: ConfigLevel::PlatformGlobal,
        // 3600, the value `metap-http`'s `TOKEN_TTL_SECONDS` const carried.
        default: || Value::from(3600),
        // Floor of 60s: anything shorter expires mid-session for a real user. Ceiling of 30 days:
        // this platform has no token revocation (audit 04 A#8), so TTL *is* the revocation window.
        validate: |v| positive_int_in(v, 60, 2_592_000),
    },
    // --- Operator: declared only so the write surface can refuse them by name ---
    ConfigKeyDef {
        key: CRON_WEBHOOK_ALLOW_PRIVATE_TARGETS,
        level: ConfigLevel::Operator,
        default: || Value::Bool(false),
        validate: |_| Err("operator-only key".to_string()),
    },
    ConfigKeyDef {
        key: CRON_WEBHOOK_ALLOWED_HOSTS,
        level: ConfigLevel::Operator,
        default: || Value::Array(vec![]),
        validate: |_| Err("operator-only key".to_string()),
    },
    ConfigKeyDef {
        key: CORS_ORIGINS,
        level: ConfigLevel::Operator,
        default: || Value::Array(vec![]),
        validate: |_| Err("operator-only key".to_string()),
    },
];

pub fn lookup(key: &str) -> Option<&'static ConfigKeyDef> {
    REGISTRY.iter().find(|d| d.key == key)
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::collections::HashSet;

    #[test]
    fn every_key_is_declared_once() {
        let mut seen = HashSet::new();
        for def in REGISTRY {
            assert!(seen.insert(def.key), "duplicate config key: {}", def.key);
        }
    }

    /// A default that its own validator rejects would mean the platform boots into a state it
    /// refuses to let anyone set — catches a bad edit to either half of a key's declaration.
    #[test]
    fn every_platform_global_default_passes_its_own_validator() {
        for def in REGISTRY.iter().filter(|d| d.level == ConfigLevel::PlatformGlobal) {
            let default = (def.default)();
            assert!(
                (def.validate)(&default).is_ok(),
                "default for {} fails its own validator: {:?}",
                def.key,
                (def.validate)(&default)
            );
        }
    }

    /// The A#1 regression in registry form: these three keys must never become writable. If someone
    /// later "promotes" one to `PlatformGlobal` for convenience, this test is what stops it.
    #[test]
    fn the_security_critical_keys_are_operator_level() {
        for key in [
            CRON_WEBHOOK_ALLOW_PRIVATE_TARGETS,
            CRON_WEBHOOK_ALLOWED_HOSTS,
            CORS_ORIGINS,
        ] {
            assert_eq!(
                lookup(key).expect("declared").level,
                ConfigLevel::Operator,
                "{key} must stay operator-only — see this module's doc comment"
            );
        }
    }

    #[test]
    fn validators_reject_zero_and_non_integers() {
        let depth = lookup(GRAPHQL_MAX_DEPTH).unwrap();
        assert!((depth.validate)(&Value::from(0)).is_err());
        assert!((depth.validate)(&Value::from("ten")).is_err());
        assert!((depth.validate)(&Value::from(10)).is_ok());
        let ttl = lookup(AUTH_SESSION_TTL_SECONDS).unwrap();
        assert!((ttl.validate)(&Value::from(30)).is_err(), "below the 60s floor");
        assert!((ttl.validate)(&Value::from(3600)).is_ok());
    }
}
