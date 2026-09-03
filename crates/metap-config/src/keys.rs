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
    /// Per-tenant, written by that tenant's own admin through `PUT /admin/config`.
    ///
    /// A `Tenant` key is *also* settable at the platform tier, and that is not a loophole but the
    /// point: `PUT /platform/config` on one of these writes the **fleet default** every tenant
    /// inherits until it sets its own value, which is the "config-global" tier the feature brief
    /// asked for. The full chain a read walks is therefore
    /// `declared default <- platform_configs <- tenant_configs`, each tier overriding only the keys
    /// it actually stores a row for.
    ///
    /// What no tier can ever reach is [`Operator`](Self::Operator) — that boundary is the one this
    /// module exists to hold.
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
    /// Whether the unauthenticated `GET /public/config` surface may return this key.
    ///
    /// Opt-in per key, and an allowlist in Rust rather than an `is_public` column for the same
    /// reason the tier is: a column deciding what may be served without authentication would itself
    /// be the thing most worth attacking. Every key defaults to `false` in practice — only the
    /// handful a login screen genuinely needs before it knows who is looking (branding) is marked
    /// `true`, and everything a public value touches is validated far more strictly than an
    /// admin-only one, because it is rendered into a page for anyone who can reach the hostname.
    pub public: bool,
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

/// Rejects anything that is not a `#rgb`/`#rrggbb` hex color.
///
/// Deliberately far stricter than "a CSS color": this value is served unauthenticated and lands in
/// a CSS custom property in the browser, so accepting arbitrary strings would hand anyone who can
/// write a tenant's config a CSS injection into that tenant's login screen. Named colors and
/// `rgb()` are rejected too — not because they are dangerous in themselves, but because allowing
/// them means parsing CSS, and a hex check is something this function can actually get right.
fn hex_color(value: &Value) -> Result<(), String> {
    let s = value.as_str().ok_or("must be a string")?;
    let hex = s.strip_prefix('#').ok_or("must start with '#'")?;
    if !matches!(hex.len(), 3 | 6) {
        return Err(format!("must be #rgb or #rrggbb, got {} hex digits", hex.len()));
    }
    if !hex.chars().all(|c| c.is_ascii_hexdigit()) {
        return Err("must contain hex digits only".to_string());
    }
    Ok(())
}

/// Accepts an absolute `https://` URL or a site-relative path (`/logo.svg`), and nothing else.
///
/// The rejections are the substance here, since this string ends up in an `<img src>` on a page
/// served to anyone: `javascript:` is script execution outright, and `data:` allows
/// `data:image/svg+xml`, which browsers treat as a document and will run script from. Plain `http:`
/// is refused as well — a logo loaded over cleartext on an HTTPS login page is a mixed-content
/// warning at best and a tamperable asset at worst.
fn logo_url(value: &Value) -> Result<(), String> {
    let s = value.as_str().ok_or("must be a string")?;
    if s.len() > 2048 {
        return Err("must be at most 2048 characters".to_string());
    }
    if s.is_empty() {
        return Ok(());
    }
    // A site-relative path, but not a protocol-relative `//evil.example/x` one, which a browser
    // resolves to an absolute URL on someone else's origin.
    if s.starts_with('/') && !s.starts_with("//") {
        return Ok(());
    }
    if s.starts_with("https://") && s.len() > "https://".len() {
        return Ok(());
    }
    Err("must be an https:// URL or a site-relative path starting with '/'".to_string())
}

/// Plain single-line text, bounded. Control characters are rejected rather than stripped — a
/// silently-altered value is harder to debug than a refused one.
fn display_text(value: &Value, max_len: usize) -> Result<(), String> {
    let s = value.as_str().ok_or("must be a string")?;
    if s.chars().count() > max_len {
        return Err(format!("must be at most {max_len} characters"));
    }
    if s.chars().any(|c| c.is_control()) {
        return Err("must not contain control characters".to_string());
    }
    Ok(())
}

pub const GRAPHQL_MAX_DEPTH: &str = "graphql.maxDepth";
pub const GRAPHQL_MAX_COMPLEXITY: &str = "graphql.maxComplexity";
pub const HTTP_RATE_LIMIT_PER_MS: &str = "http.rateLimitPerMillisecond";
pub const HTTP_RATE_LIMIT_BURST: &str = "http.rateLimitBurst";
pub const AUTH_SESSION_TTL_SECONDS: &str = "auth.sessionTtlSeconds";
pub const THEME_PRIMARY_COLOR: &str = "theme.primaryColor";
pub const THEME_LOGO_URL: &str = "theme.logoUrl";
pub const THEME_DISPLAY_NAME: &str = "theme.displayName";
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
        public: false,
    },
    ConfigKeyDef {
        key: GRAPHQL_MAX_COMPLEXITY,
        level: ConfigLevel::PlatformGlobal,
        default: || Value::from(1000),
        validate: |v| positive_int_in(v, 10, 1_000_000),
        public: false,
    },
    ConfigKeyDef {
        key: HTTP_RATE_LIMIT_PER_MS,
        level: ConfigLevel::PlatformGlobal,
        default: || Value::from(200),
        validate: |v| positive_int_in(v, 1, 60_000),
        public: false,
    },
    ConfigKeyDef {
        key: HTTP_RATE_LIMIT_BURST,
        level: ConfigLevel::PlatformGlobal,
        default: || Value::from(300),
        validate: |v| positive_int_in(v, 1, 100_000),
        public: false,
    },
    // --- Tenant: a fleet default a platform admin sets, that each tenant may override ---
    ConfigKeyDef {
        key: AUTH_SESSION_TTL_SECONDS,
        // Tenant, not PlatformGlobal: how long its own users stay signed in is a policy call a
        // tenant legitimately makes for itself, and it only ever affects that tenant's own users.
        // The bounds below stay operator-controlled, which is what keeps this from being a security
        // decision a tenant admin can get catastrophically wrong. Not public — the login screen has
        // no use for it, and an unauthenticated caller learning a tenant's session lifetime is
        // free reconnaissance for nothing in return.
        level: ConfigLevel::Tenant,
        // 3600, the value `metap-http`'s `TOKEN_TTL_SECONDS` const carried.
        default: || Value::from(3600),
        // Floor of 60s: anything shorter expires mid-session for a real user. Ceiling of 30 days:
        // this platform has no token revocation (audit 04 A#8), so TTL *is* the revocation window.
        validate: |v| positive_int_in(v, 60, 2_592_000),
        public: false,
    },
    // --- Tenant + public: branding a login screen must render before it knows who is looking ---
    ConfigKeyDef {
        key: THEME_PRIMARY_COLOR,
        level: ConfigLevel::Tenant,
        // Empty means "no override" for the string-valued theme keys, which is what lets the
        // frontend keep its own design-system default rather than having this table dictate one.
        default: || Value::from(""),
        validate: |v| {
            if v.as_str() == Some("") {
                return Ok(());
            }
            hex_color(v)
        },
        public: true,
    },
    ConfigKeyDef {
        key: THEME_LOGO_URL,
        level: ConfigLevel::Tenant,
        default: || Value::from(""),
        validate: logo_url,
        public: true,
    },
    ConfigKeyDef {
        key: THEME_DISPLAY_NAME,
        level: ConfigLevel::Tenant,
        default: || Value::from(""),
        validate: |v| display_text(v, 64),
        public: true,
    },
    // --- Operator: declared only so the write surface can refuse them by name ---
    ConfigKeyDef {
        key: CRON_WEBHOOK_ALLOW_PRIVATE_TARGETS,
        level: ConfigLevel::Operator,
        default: || Value::Bool(false),
        validate: |_| Err("operator-only key".to_string()),
        public: false,
    },
    ConfigKeyDef {
        key: CRON_WEBHOOK_ALLOWED_HOSTS,
        level: ConfigLevel::Operator,
        default: || Value::Array(vec![]),
        validate: |_| Err("operator-only key".to_string()),
        public: false,
    },
    ConfigKeyDef {
        key: CORS_ORIGINS,
        level: ConfigLevel::Operator,
        default: || Value::Array(vec![]),
        validate: |_| Err("operator-only key".to_string()),
        public: false,
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
    ///
    /// `Operator` keys are excluded because their validator rejects everything by design; every
    /// other tier is covered, so adding a key at any writable tier is covered automatically.
    #[test]
    fn every_writable_default_passes_its_own_validator() {
        for def in REGISTRY.iter().filter(|d| d.level != ConfigLevel::Operator) {
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

    /// Only branding is public, and only at the tenant tier. A platform-wide or operator key
    /// marked public would be served to anyone who can reach the deployment.
    #[test]
    fn only_tenant_tier_keys_are_ever_public() {
        for def in REGISTRY.iter().filter(|d| d.public) {
            assert_eq!(
                def.level,
                ConfigLevel::Tenant,
                "{} is served unauthenticated but is not a tenant-tier key",
                def.key
            );
        }
    }

    /// These two validators are the security substance of the theme feature: the values they guard
    /// are served without authentication and rendered into a page, so a permissive one is an
    /// injection into every login screen the deployment serves.
    #[test]
    fn the_public_theme_validators_reject_injection_shaped_values() {
        let color = lookup(THEME_PRIMARY_COLOR).unwrap();
        assert!((color.validate)(&Value::from("#0af")).is_ok());
        assert!((color.validate)(&Value::from("#123456")).is_ok());
        assert!((color.validate)(&Value::from("")).is_ok(), "empty means 'no override'");
        for rejected in [
            "red",
            "rgb(1,2,3)",
            "#0af; background: url(https://evil.example/x)",
            "#12345",
            "#zzzzzz",
            "var(--anything)",
        ] {
            assert!(
                (color.validate)(&Value::from(rejected)).is_err(),
                "{rejected:?} must not be storable as a theme colour"
            );
        }

        let logo = lookup(THEME_LOGO_URL).unwrap();
        assert!((logo.validate)(&Value::from("https://cdn.example.com/logo.svg")).is_ok());
        assert!((logo.validate)(&Value::from("/assets/logo.svg")).is_ok());
        assert!((logo.validate)(&Value::from("")).is_ok());
        for rejected in [
            "javascript:alert(1)",
            "data:image/svg+xml;base64,PHN2Zz48c2NyaXB0Pg==",
            "//evil.example/logo.svg",
            "http://cdn.example.com/logo.svg",
            "HTTPS://cdn.example.com/logo.svg",
        ] {
            assert!(
                (logo.validate)(&Value::from(rejected)).is_err(),
                "{rejected:?} must not be storable as a logo URL"
            );
        }

        let name = lookup(THEME_DISPLAY_NAME).unwrap();
        assert!((name.validate)(&Value::from("Acme Corp")).is_ok());
        assert!((name.validate)(&Value::from("line\nbreak")).is_err());
        assert!((name.validate)(&Value::from("x".repeat(65))).is_err());
        assert!((name.validate)(&Value::from(42)).is_err());
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
