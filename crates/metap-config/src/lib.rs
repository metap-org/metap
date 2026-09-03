//! Platform configuration held in Postgres instead of hard-coded in Rust or pinned to an env var
//! at boot (`docs/features/18-config-tiers-db-backed.md`, slice 1).
//!
//! **What this crate is for.** Three numbers that shape how the platform behaves in production —
//! GraphQL depth/complexity limits, the HTTP rate limit, and session TTL — were literals sitting in
//! `metap-http`/`graphql-gateway` source. Audit 04 A#7 flagged the GraphQL pair specifically
//! ("hardcode `SchemaLimits::default()`, không chỉnh qua env"), and `SchemaLimits::default()`'s own
//! doc comment had been saying "starting guardrails, not a permanent tuning" the whole time. There
//! was no way to retune any of them short of a rebuild and redeploy.
//!
//! **What it is deliberately not.** Not a free-form key/value bag. Every settable key is declared
//! in [`keys::REGISTRY`] with a tier, a default and a validator; an unknown key is rejected rather
//! than stored. A platform whose entire thesis is "validation is generated from declared metadata"
//! should not grow a config table that validates nothing — that table becomes a junk drawer within
//! months, and nothing can then be safely read out of it.
//!
//! **Tiering is the security boundary**, not a convenience. See [`keys`]'s doc comment: the
//! operator-controlled SSRF settings from audit 04 A#1 are declared `Operator` so that no API,
//! including the platform-admin one, can write them.
//!
//! **Reads never touch the database.** [`ConfigStore`] loads the table once at boot into an
//! `ArcSwap` snapshot and swaps a fresh one in after every write, the same hot-swap shape
//! `MetadataRegistry` already uses for entity definitions. A caller reading a limit on the request
//! path pays an atomic load, not a query — which is what makes it safe to consult these from
//! middleware that runs on every request.
//!
//! No HTTP and no business-entity knowledge — a plain library, same shape as `metap-cron` and
//! `metap-dashboards`. The HTTP surface lives in `metap-http`'s `routes/platform_config.rs`.

pub mod keys;

use std::collections::HashMap;
use std::sync::Arc;

use arc_swap::ArcSwap;
use serde_json::Value;
use sqlx::PgPool;

pub use keys::{ConfigKeyDef, ConfigLevel};

/// An immutable view of every stored override. Values absent here fall back to the key's declared
/// default, so this map only ever holds keys someone explicitly set.
#[derive(Debug, Default)]
pub struct ConfigSnapshot {
    overrides: HashMap<String, Value>,
}

impl ConfigSnapshot {
    /// The effective value for a key: the stored override if there is one, else the declared
    /// default. Panics only for a key that isn't in the registry at all — that is a programming
    /// error (a typo'd constant), not a runtime condition, and failing loudly at the call site
    /// beats silently reading `null` into a limit.
    pub fn get(&self, key: &str) -> Value {
        if let Some(value) = self.overrides.get(key) {
            return value.clone();
        }
        let def = keys::lookup(key).unwrap_or_else(|| panic!("config key {key:?} is not in the registry"));
        (def.default)()
    }

    /// Convenience for the numeric keys, which is all of slice 1's. Falls back to the declared
    /// default if a stored value somehow isn't a number — a row written before a validator tightened
    /// shouldn't be able to take a limit out entirely.
    pub fn get_u64(&self, key: &str) -> u64 {
        let value = self.get(key);
        value.as_u64().unwrap_or_else(|| {
            tracing::warn!(
                key,
                ?value,
                "stored config value is not a positive integer; using default"
            );
            let def = keys::lookup(key).expect("registry key");
            (def.default)().as_u64().expect("numeric key's default is numeric")
        })
    }

    /// Every `PlatformGlobal` key with its effective value — what `GET /platform/config` renders.
    /// `Operator` keys are excluded on purpose: an API that cannot write them should not advertise
    /// their values either.
    pub fn platform_global_view(&self) -> Vec<(&'static str, Value)> {
        keys::REGISTRY
            .iter()
            .filter(|d| d.level == ConfigLevel::PlatformGlobal)
            .map(|d| (d.key, self.get(d.key)))
            .collect()
    }
}

/// Why a config write was refused. Hand-rolled rather than `thiserror`-derived to match
/// `metap_control::RouterError` and every other typed error in this workspace — no crate here
/// carries `thiserror`, and one new dependency for one enum is not worth breaking that.
#[derive(Debug)]
pub enum ConfigError {
    UnknownKey(String),
    /// The key exists but is not writable at the tier the caller is acting on.
    NotWritable {
        key: String,
        reason: String,
    },
    Invalid {
        key: String,
        reason: String,
    },
    Db(sqlx::Error),
}

impl std::fmt::Display for ConfigError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            Self::UnknownKey(key) => write!(f, "unknown config key {key:?}"),
            Self::NotWritable { key, reason } => write!(f, "config key {key:?} is not writable here: {reason}"),
            Self::Invalid { key, reason } => write!(f, "invalid value for {key:?}: {reason}"),
            Self::Db(e) => write!(f, "config storage error: {e}"),
        }
    }
}

impl std::error::Error for ConfigError {}

impl From<sqlx::Error> for ConfigError {
    fn from(e: sqlx::Error) -> Self {
        Self::Db(e)
    }
}

/// Loads and caches `platform_configs`, and is the only writer of it.
pub struct ConfigStore {
    pool: PgPool,
    snapshot: ArcSwap<ConfigSnapshot>,
}

impl ConfigStore {
    /// A store that has not read the table yet: every key reads back its declared default.
    ///
    /// Exists because `AppState::new` is synchronous and this crate's load is not. Constructing
    /// this is safe rather than a half-initialized state — "no overrides" is exactly the behavior
    /// the platform had before this table existed, so a host binary that never calls
    /// [`reload`](Self::reload) keeps working precisely as it did, just without the ability to
    /// retune anything at runtime.
    pub fn unloaded(pool: PgPool) -> Self {
        Self {
            pool,
            snapshot: ArcSwap::from_pointee(ConfigSnapshot::default()),
        }
    }

    /// Re-reads the table into a fresh snapshot. Call once at boot, after `AppState::new` and
    /// before `build_router`, so the router picks up stored values instead of defaults.
    pub async fn reload(&self) -> Result<(), sqlx::Error> {
        self.snapshot.store(Arc::new(Self::read_snapshot(&self.pool).await?));
        Ok(())
    }

    /// Reads the whole table once. Called at boot, before the router is built, so a caller can
    /// treat [`current`](Self::current) as always populated.
    pub async fn load(pool: PgPool) -> Result<Self, sqlx::Error> {
        let snapshot = Self::read_snapshot(&pool).await?;
        Ok(Self {
            pool,
            snapshot: ArcSwap::from_pointee(snapshot),
        })
    }

    async fn read_snapshot(pool: &PgPool) -> Result<ConfigSnapshot, sqlx::Error> {
        let rows: Vec<(String, Value)> = sqlx::query_as("SELECT key, value FROM platform_configs")
            .fetch_all(pool)
            .await?;
        // A row whose key is no longer declared (a key removed in a later release, a hand-edited
        // row) is dropped rather than surfaced — `get` would ignore it anyway, and keeping it in the
        // snapshot would let it shadow a default if the key were ever re-declared with a new shape.
        let overrides = rows
            .into_iter()
            .filter(|(key, _)| {
                let known = keys::lookup(key).is_some();
                if !known {
                    tracing::warn!(key, "ignoring stored config row for a key not in the registry");
                }
                known
            })
            .collect();
        Ok(ConfigSnapshot { overrides })
    }

    pub fn current(&self) -> Arc<ConfigSnapshot> {
        self.snapshot.load_full()
    }

    /// Writes one `PlatformGlobal` key and swaps in a fresh snapshot, so the change takes effect
    /// without a restart.
    ///
    /// Rejects, in this order: an unknown key, a key whose tier isn't `PlatformGlobal` (an
    /// `Operator` key most importantly — see [`keys`]'s doc comment), then a value its own
    /// validator refuses. Tier is checked before validity on purpose: an operator-only key must
    /// report *why* it can't be written rather than leaking whether the proposed value would have
    /// been acceptable.
    pub async fn set_platform_global(&self, key: &str, value: Value) -> Result<(), ConfigError> {
        let def = keys::lookup(key).ok_or_else(|| ConfigError::UnknownKey(key.to_string()))?;
        match def.level {
            ConfigLevel::PlatformGlobal => {}
            ConfigLevel::Operator => {
                return Err(ConfigError::NotWritable {
                    key: key.to_string(),
                    reason: "operator-only, settable through deployment configuration only".to_string(),
                })
            }
            ConfigLevel::Tenant => {
                return Err(ConfigError::NotWritable {
                    key: key.to_string(),
                    reason: "tenant-scoped, set it through the tenant's own config surface".to_string(),
                })
            }
        }
        (def.validate)(&value).map_err(|reason| ConfigError::Invalid {
            key: key.to_string(),
            reason,
        })?;

        sqlx::query(
            "INSERT INTO platform_configs (key, value) VALUES ($1, $2)
             ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()",
        )
        .bind(key)
        .bind(&value)
        .execute(&self.pool)
        .await?;

        self.snapshot.store(Arc::new(Self::read_snapshot(&self.pool).await?));
        tracing::info!(key, "platform config updated");
        Ok(())
    }

    /// Removes an override so the key falls back to its declared default.
    pub async fn reset_platform_global(&self, key: &str) -> Result<(), ConfigError> {
        let def = keys::lookup(key).ok_or_else(|| ConfigError::UnknownKey(key.to_string()))?;
        if def.level != ConfigLevel::PlatformGlobal {
            return Err(ConfigError::NotWritable {
                key: key.to_string(),
                reason: "not a platform-global key".to_string(),
            });
        }
        sqlx::query("DELETE FROM platform_configs WHERE key = $1")
            .bind(key)
            .execute(&self.pool)
            .await?;
        self.snapshot.store(Arc::new(Self::read_snapshot(&self.pool).await?));
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::keys::{AUTH_SESSION_TTL_SECONDS, GRAPHQL_MAX_DEPTH};

    fn snapshot_with(pairs: &[(&str, Value)]) -> ConfigSnapshot {
        ConfigSnapshot {
            overrides: pairs.iter().map(|(k, v)| (k.to_string(), v.clone())).collect(),
        }
    }

    #[test]
    fn an_unset_key_reads_back_the_declared_default_not_null() {
        let empty = ConfigSnapshot::default();
        assert_eq!(empty.get_u64(GRAPHQL_MAX_DEPTH), 10);
        assert_eq!(empty.get_u64(AUTH_SESSION_TTL_SECONDS), 3600);
    }

    #[test]
    fn a_stored_override_wins_over_the_default() {
        let snapshot = snapshot_with(&[(GRAPHQL_MAX_DEPTH, Value::from(25))]);
        assert_eq!(snapshot.get_u64(GRAPHQL_MAX_DEPTH), 25);
        // Untouched keys keep their defaults.
        assert_eq!(snapshot.get_u64(AUTH_SESSION_TTL_SECONDS), 3600);
    }

    /// A malformed stored value must degrade to the default, never to zero — a `burst_size` of 0
    /// would reject every request on the platform.
    #[test]
    fn a_non_numeric_stored_value_falls_back_instead_of_zeroing_a_limit() {
        let snapshot = snapshot_with(&[(GRAPHQL_MAX_DEPTH, Value::from("not a number"))]);
        assert_eq!(snapshot.get_u64(GRAPHQL_MAX_DEPTH), 10);
    }

    #[test]
    fn the_platform_global_view_never_includes_operator_keys() {
        let view = ConfigSnapshot::default().platform_global_view();
        let shown: Vec<&str> = view.iter().map(|(k, _)| *k).collect();
        assert!(shown.contains(&GRAPHQL_MAX_DEPTH));
        for operator_key in [
            keys::CRON_WEBHOOK_ALLOW_PRIVATE_TARGETS,
            keys::CRON_WEBHOOK_ALLOWED_HOSTS,
            keys::CORS_ORIGINS,
        ] {
            assert!(
                !shown.contains(&operator_key),
                "{operator_key} must not be exposed by the platform-admin surface"
            );
        }
    }
}
