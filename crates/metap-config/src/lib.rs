//! Platform configuration held in Postgres instead of hard-coded in Rust or pinned to an env var
//! at boot (`docs/features/18-config-tiers-db-backed.md`, slices 1-2).
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
//! **Three tiers, resolved in one direction.** A read walks
//! `default declared in Rust <- platform_configs <- tenant_configs`, each tier overriding only the
//! keys it actually stores a row for ([`EffectiveConfig`]). Which tiers a given key participates in
//! is the key's own declaration, not the caller's: a `PlatformGlobal` key binds every tenant, a
//! `Tenant` key takes a fleet default from the platform tier that each tenant may then override,
//! and an `Operator` key takes part in neither.
//!
//! **Reads never touch the database.** [`ConfigStore`] loads the table once at boot into an
//! `ArcSwap` snapshot and swaps a fresh one in after every write, the same hot-swap shape
//! `MetadataRegistry` already uses for entity definitions. A caller reading a limit on the request
//! path pays an atomic load, not a query — which is what makes it safe to consult these from
//! middleware that runs on every request.
//!
//! No HTTP and no business-entity knowledge — a plain library, same shape as `metap-cron` and
//! `metap-dashboards`. The HTTP surfaces live in `metap-http`: `routes/platform_config.rs`
//! (`/platform/config`), `routes/tenant_config.rs` (`/admin/config`, and the unauthenticated
//! `/public/config` that serves only keys declared `public`).

pub mod keys;

use std::collections::HashMap;
use std::sync::Arc;

use arc_swap::ArcSwap;
use moka::future::Cache;
use serde_json::Value;
use sqlx::{PgExecutor, PgPool};
use uuid::Uuid;

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

    /// Every key a platform admin may write, with its current fleet-wide value — what
    /// `GET /platform/config` renders.
    ///
    /// That is both tiers this surface can set: `PlatformGlobal` keys, and `Tenant` keys whose
    /// platform-tier row is the **fleet default** each tenant inherits until it overrides it. The
    /// `level` comes back with each entry so a platform admin can tell which of the two a key is
    /// — setting a fleet default that half the tenants already override is very different from
    /// setting a value that binds everyone.
    ///
    /// `Operator` keys are excluded on purpose: an API that cannot write them should not advertise
    /// their values either.
    pub fn platform_writable_view(&self) -> Vec<(&'static ConfigKeyDef, Value)> {
        keys::REGISTRY
            .iter()
            .filter(|d| matches!(d.level, ConfigLevel::PlatformGlobal | ConfigLevel::Tenant))
            .map(|d| (d, self.get(d.key)))
            .collect()
    }
}

/// One tenant's own overrides — the top tier of the chain
/// `declared default <- platform_configs <- tenant_configs`.
///
/// Holds only keys that tenant explicitly set. Everything else falls through to [`ConfigSnapshot`],
/// which is why this type is never read on its own: [`EffectiveConfig`] pairs the two.
#[derive(Debug, Default)]
pub struct TenantConfigSnapshot {
    overrides: HashMap<String, Value>,
}

/// A tenant's effective configuration: their own overrides layered over the fleet snapshot.
///
/// Built per read rather than stored, because the two halves have different lifetimes — the
/// platform snapshot is process-wide and swapped on write, a tenant's is cached per tenant with a
/// TTL. Cloning one is two `Arc` bumps.
pub struct EffectiveConfig {
    platform: Arc<ConfigSnapshot>,
    tenant: Option<Arc<TenantConfigSnapshot>>,
}

impl EffectiveConfig {
    pub fn new(platform: Arc<ConfigSnapshot>, tenant: Option<Arc<TenantConfigSnapshot>>) -> Self {
        Self { platform, tenant }
    }

    /// Tenant override, else the fleet default, else the value declared in Rust.
    pub fn get(&self, key: &str) -> Value {
        if let Some(tenant) = &self.tenant {
            if let Some(value) = tenant.overrides.get(key) {
                return value.clone();
            }
        }
        self.platform.get(key)
    }

    /// Same fallback discipline as [`ConfigSnapshot::get_u64`] — a malformed stored value degrades
    /// to the declared default rather than to zero.
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

    /// Every `Tenant` key with its effective value, plus whether *this* tenant set it — what
    /// `GET /admin/config` renders. A key showing `overridden: false` is inherited, so a tenant
    /// admin can tell "we chose this" from "the platform chose this for us".
    pub fn tenant_view(&self) -> Vec<(&'static ConfigKeyDef, Value, bool)> {
        keys::REGISTRY
            .iter()
            .filter(|d| d.level == ConfigLevel::Tenant)
            .map(|d| {
                let overridden = self.tenant.as_ref().is_some_and(|t| t.overrides.contains_key(d.key));
                (d, self.get(d.key), overridden)
            })
            .collect()
    }

    /// The stored reference for a secret key, or `None` when this tenant has no credential.
    ///
    /// There is no counterpart returning a value, and that is the point: the plaintext is not in
    /// this crate's data at all, so no read path here could return it even by mistake.
    pub fn secret_ref(&self, key: &str) -> Option<String> {
        self.get(key)
            .get("secretRef")
            .and_then(|v| v.as_str())
            .map(str::to_string)
    }

    /// Only the keys declared `public` — what the unauthenticated hostname-resolved surface
    /// returns, and the *only* thing it may ever return.
    ///
    /// Driven off `ConfigKeyDef::public` rather than a caller-supplied list so that adding a key to
    /// the registry can never accidentally publish it: a new key is private unless its declaration
    /// says otherwise.
    pub fn public_view(&self) -> Vec<(&'static str, Value)> {
        keys::REGISTRY
            .iter()
            .filter(|d| d.public)
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

/// Default lifetime of a cached tenant snapshot. Matches `metap_control::RegistryCache`'s 30s for
/// the same reason: it is short enough that a stale value is a nuisance rather than a bug, and long
/// enough that the unauthenticated theme endpoint cannot be used to hammer the database.
pub const TENANT_CACHE_TTL: std::time::Duration = std::time::Duration::from_secs(30);

/// Loads and caches `platform_configs`, and is the only writer of it and of `tenant_configs`.
///
/// The two tiers are cached differently on purpose. The platform tier is a single `ArcSwap`
/// re-read after every write — one snapshot for the whole process, no expiry. The tenant tier is a
/// `moka` cache keyed by tenant with [`TENANT_CACHE_TTL`], because there is no bound on how many
/// tenants a deployment has and holding every one of them forever is not a cache, it is a leak.
///
/// A consequence worth stating plainly: in a multi-instance deployment a write on one instance is
/// visible to the others only after the TTL (tenant tier) or not until restart (platform tier).
/// Both tiers hold admin-set configuration that changes rarely, and the alternative is a
/// cross-process invalidation channel this platform does not yet have anywhere else either.
pub struct ConfigStore {
    pool: PgPool,
    snapshot: ArcSwap<ConfigSnapshot>,
    tenants: Cache<Uuid, Arc<TenantConfigSnapshot>>,
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
            tenants: Cache::builder().time_to_live(TENANT_CACHE_TTL).build(),
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
            tenants: Cache::builder().time_to_live(TENANT_CACHE_TTL).build(),
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
    /// Accepts both tiers this surface owns: a `PlatformGlobal` key (binding on everyone) and a
    /// `Tenant` key, where the platform row is the **fleet default** tenants inherit until they
    /// override it. What it refuses is `Operator` — see [`keys`]'s doc comment.
    ///
    /// Tier is checked before validity on purpose: an operator-only key must report *why* it can't
    /// be written rather than leaking whether the proposed value would have been acceptable.
    pub async fn set_platform_global(&self, key: &str, value: Value) -> Result<(), ConfigError> {
        let def = keys::lookup(key).ok_or_else(|| ConfigError::UnknownKey(key.to_string()))?;
        match def.level {
            ConfigLevel::PlatformGlobal | ConfigLevel::Tenant => {}
            ConfigLevel::Operator => {
                return Err(ConfigError::NotWritable {
                    key: key.to_string(),
                    reason: "operator-only, settable through deployment configuration only".to_string(),
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
        if def.level == ConfigLevel::Operator {
            return Err(ConfigError::NotWritable {
                key: key.to_string(),
                reason: "operator-only, settable through deployment configuration only".to_string(),
            });
        }
        sqlx::query("DELETE FROM platform_configs WHERE key = $1")
            .bind(key)
            .execute(&self.pool)
            .await?;
        self.snapshot.store(Arc::new(Self::read_snapshot(&self.pool).await?));
        Ok(())
    }

    // --- tenant tier (slice 2) ---
    //
    // These take an executor rather than resolving the tenant's database themselves. `tenant_configs`
    // is tenant-scoped like `dashboard_configs`/`tenant_auth_configs`, so it must be reached through
    // `metap_control::Router::begin(tenant)` — and this crate deliberately does not depend on
    // `metap-control` (a plain library, same shape as `metap-cron`; that dependency would also drag
    // Vault/AWS/GCP clients into every consumer). The caller opens the transaction; this crate owns
    // the caching, validation and tier rules.

    /// The cached snapshot for a tenant, or `None` if it isn't cached.
    ///
    /// Separate from [`load_tenant`](Self::load_tenant) so a caller can answer a read **without
    /// opening a tenant transaction at all** — `Router::begin` is itself a database round trip, so a
    /// `try_get_with`-style API that took an executor would pay for one on every cache hit, which is
    /// exactly what the caching is meant to avoid.
    /// `async` only because `moka`'s future cache is; it performs no I/O and never waits on a
    /// database.
    pub async fn cached_tenant(&self, tenant_id: Uuid) -> Option<Arc<TenantConfigSnapshot>> {
        self.tenants.get(&tenant_id).await
    }

    /// Reads one tenant's overrides through the caller-supplied tenant-routed executor and caches
    /// the result.
    pub async fn load_tenant<'e>(
        &self,
        executor: impl PgExecutor<'e>,
        tenant_id: Uuid,
    ) -> Result<Arc<TenantConfigSnapshot>, sqlx::Error> {
        let rows: Vec<(String, Value)> = sqlx::query_as("SELECT key, value FROM tenant_configs WHERE tenant_id = $1")
            .bind(tenant_id)
            .fetch_all(executor)
            .await?;
        // Same discipline as the platform tier: a row whose key is no longer declared `Tenant` is
        // dropped rather than honored. That matters more here — it means demoting a key to
        // `Operator` in a later release immediately stops any row a tenant had already written from
        // having any effect, without needing a data migration to go find them.
        let overrides = rows
            .into_iter()
            .filter(|(key, _)| match keys::lookup(key) {
                Some(def) if def.level == ConfigLevel::Tenant => true,
                Some(_) => {
                    tracing::warn!(
                        key,
                        "ignoring tenant config row for a key that is no longer tenant-writable"
                    );
                    false
                }
                None => {
                    tracing::warn!(key, "ignoring tenant config row for a key not in the registry");
                    false
                }
            })
            .collect();
        let snapshot = Arc::new(TenantConfigSnapshot { overrides });
        self.tenants.insert(tenant_id, snapshot.clone()).await;
        Ok(snapshot)
    }

    /// Pairs a tenant's overrides with the fleet snapshot. `tenant` is `None` for a caller that has
    /// no tenant at all, which reads back the fleet-wide values.
    pub fn effective(&self, tenant: Option<Arc<TenantConfigSnapshot>>) -> EffectiveConfig {
        EffectiveConfig::new(self.current(), tenant)
    }

    /// Writes one tenant's override for a `Tenant`-tier key.
    ///
    /// The `tenant_id` bound into the statement comes from the caller's verified identity, never
    /// from a request body — see `metap-http`'s `routes/tenant_config.rs`. Refuses any key not
    /// declared `Tenant`: a tenant admin reaching a `PlatformGlobal` key would be setting fleet
    /// policy from inside one tenant, and an `Operator` key is unreachable from anywhere.
    pub async fn set_tenant<'e>(
        &self,
        executor: impl PgExecutor<'e>,
        tenant_id: Uuid,
        key: &str,
        value: Value,
    ) -> Result<(), ConfigError> {
        let def = self.tenant_writable_key(key)?;
        // A secret key's value never travels this path — the plaintext goes to `SecretStore` and
        // only `{"secretRef": ...}` is stored, via `set_tenant_secret_marker`. Refusing here rather
        // than trusting callers is what keeps "the config table never holds a credential" a
        // property of this crate instead of a convention its users must remember.
        if def.secret {
            return Err(ConfigError::NotWritable {
                key: key.to_string(),
                reason: "holds a credential; write it through the secret path, not as a config value".to_string(),
            });
        }
        (def.validate)(&value).map_err(|reason| ConfigError::Invalid {
            key: key.to_string(),
            reason,
        })?;
        sqlx::query(
            "INSERT INTO tenant_configs (tenant_id, key, value) VALUES ($1, $2, $3)
             ON CONFLICT (tenant_id, key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()",
        )
        .bind(tenant_id)
        .bind(key)
        .bind(&value)
        .execute(executor)
        .await?;
        self.tenants.invalidate(&tenant_id).await;
        tracing::info!(%tenant_id, key, "tenant config updated");
        Ok(())
    }

    /// Clears a tenant's override so the key falls back to the fleet default, then to the declared
    /// one.
    pub async fn reset_tenant<'e>(
        &self,
        executor: impl PgExecutor<'e>,
        tenant_id: Uuid,
        key: &str,
    ) -> Result<(), ConfigError> {
        self.tenant_writable_key(key)?;
        sqlx::query("DELETE FROM tenant_configs WHERE tenant_id = $1 AND key = $2")
            .bind(tenant_id)
            .bind(key)
            .execute(executor)
            .await?;
        self.tenants.invalidate(&tenant_id).await;
        Ok(())
    }

    /// Validates a proposed **plaintext** credential for a secret key and hands back its
    /// declaration, without storing anything.
    ///
    /// Split out because the store cannot do the write itself: the value belongs in
    /// `metap_control::SecretStore`, and this crate deliberately does not depend on `metap-control`
    /// (see the tenant-tier note above). The HTTP layer, which has both, validates here, writes the
    /// credential there, then records the marker with
    /// [`set_tenant_secret_marker`](Self::set_tenant_secret_marker).
    pub fn validate_tenant_secret(&self, key: &str, plaintext: &Value) -> Result<&'static ConfigKeyDef, ConfigError> {
        let def = self.tenant_writable_key(key)?;
        if !def.secret {
            return Err(ConfigError::NotWritable {
                key: key.to_string(),
                reason: "is not a credential; write it as an ordinary config value".to_string(),
            });
        }
        (def.validate)(plaintext).map_err(|reason| ConfigError::Invalid {
            key: key.to_string(),
            reason,
        })?;
        Ok(def)
    }

    /// Records that a tenant has a credential stored for `key`, as `{"secretRef": ...}`.
    ///
    /// `secret_ref` must be the server-derived `metap_control::tenant_secret_ref` value. Nothing
    /// here can check that — which is exactly why the derivation takes no caller input at all, so
    /// there is no untrusted string for this function to have to distrust.
    pub async fn set_tenant_secret_marker<'e>(
        &self,
        executor: impl PgExecutor<'e>,
        tenant_id: Uuid,
        key: &str,
        secret_ref: &str,
    ) -> Result<(), ConfigError> {
        let def = self.tenant_writable_key(key)?;
        if !def.secret {
            return Err(ConfigError::NotWritable {
                key: key.to_string(),
                reason: "is not a credential".to_string(),
            });
        }
        let marker = serde_json::json!({ "secretRef": secret_ref });
        sqlx::query(
            "INSERT INTO tenant_configs (tenant_id, key, value) VALUES ($1, $2, $3)
             ON CONFLICT (tenant_id, key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()",
        )
        .bind(tenant_id)
        .bind(key)
        .bind(&marker)
        .execute(executor)
        .await?;
        self.tenants.invalidate(&tenant_id).await;
        // Deliberately not logging `secret_ref`: it is a pure function of `tenant_id` and `key`,
        // both already on this line, so it adds nothing a reader could not derive — while putting a
        // secret-store lookup key into log storage. CodeQL flagged the earlier version of this line
        // (cleartext-logging), and on inspection it was right that the field earned nothing.
        tracing::info!(%tenant_id, key, "tenant credential stored");
        Ok(())
    }

    fn tenant_writable_key(&self, key: &str) -> Result<&'static ConfigKeyDef, ConfigError> {
        let def = keys::lookup(key).ok_or_else(|| ConfigError::UnknownKey(key.to_string()))?;
        match def.level {
            ConfigLevel::Tenant => Ok(def),
            ConfigLevel::Operator => Err(ConfigError::NotWritable {
                key: key.to_string(),
                reason: "operator-only, settable through deployment configuration only".to_string(),
            }),
            ConfigLevel::PlatformGlobal => Err(ConfigError::NotWritable {
                key: key.to_string(),
                reason: "fleet-wide, settable only by a platform administrator".to_string(),
            }),
        }
    }

    /// Drops a tenant's cached snapshot. For a caller that wrote `tenant_configs` through some path
    /// other than [`set_tenant`](Self::set_tenant) — a data fix, a test — and needs the next read to
    /// see it.
    pub async fn invalidate_tenant(&self, tenant_id: Uuid) {
        self.tenants.invalidate(&tenant_id).await;
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

    fn tenant_with(pairs: &[(&str, Value)]) -> Arc<TenantConfigSnapshot> {
        Arc::new(TenantConfigSnapshot {
            overrides: pairs.iter().map(|(k, v)| (k.to_string(), v.clone())).collect(),
        })
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
    fn the_platform_writable_view_never_includes_operator_keys() {
        let view = ConfigSnapshot::default().platform_writable_view();
        let shown: Vec<&str> = view.iter().map(|(d, _)| d.key).collect();
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
        // A tenant-tier key *is* listed here: the platform row is that key's fleet default.
        assert!(shown.contains(&keys::THEME_PRIMARY_COLOR));
    }

    // --- the three-tier resolution chain (slice 2) ---

    /// The whole point of the tenant tier: each level overrides only what it actually stores, and
    /// anything it doesn't falls through to the one below rather than to `null`.
    #[test]
    fn each_tier_overrides_only_the_keys_it_stores() {
        let platform = Arc::new(snapshot_with(&[
            (AUTH_SESSION_TTL_SECONDS, Value::from(7200)),
            (keys::THEME_PRIMARY_COLOR, Value::from("#0af")),
        ]));
        let tenant = tenant_with(&[(keys::THEME_PRIMARY_COLOR, Value::from("#123456"))]);
        let effective = EffectiveConfig::new(platform, Some(tenant));

        // Tenant wins where it set something...
        assert_eq!(effective.get(keys::THEME_PRIMARY_COLOR), Value::from("#123456"));
        // ...the fleet default where it didn't...
        assert_eq!(effective.get_u64(AUTH_SESSION_TTL_SECONDS), 7200);
        // ...and the value declared in Rust where neither did.
        assert_eq!(effective.get_u64(GRAPHQL_MAX_DEPTH), 10);
    }

    #[test]
    fn a_tenant_that_set_nothing_reads_the_fleet_defaults() {
        let platform = Arc::new(snapshot_with(&[(AUTH_SESSION_TTL_SECONDS, Value::from(900))]));
        let effective = EffectiveConfig::new(platform, Some(tenant_with(&[])));
        assert_eq!(effective.get_u64(AUTH_SESSION_TTL_SECONDS), 900);
        assert!(effective.tenant_view().iter().all(|(_, _, overridden)| !overridden));
    }

    /// **The public-surface boundary.** `public_view` is what an unauthenticated caller receives, so
    /// it must be driven by the registry's own flag — not by tier, and not by anything a request
    /// can influence. A tenant-tier key that isn't branding must not appear even though the tenant
    /// set it and even though the caller reached the right hostname.
    #[test]
    fn the_public_view_contains_only_keys_declared_public() {
        let tenant = tenant_with(&[
            (keys::THEME_DISPLAY_NAME, Value::from("Acme")),
            (AUTH_SESSION_TTL_SECONDS, Value::from(600)),
        ]);
        let effective = EffectiveConfig::new(Arc::new(ConfigSnapshot::default()), Some(tenant));
        let public: Vec<&str> = effective.public_view().iter().map(|(k, _)| *k).collect();

        assert!(public.contains(&keys::THEME_DISPLAY_NAME));
        assert!(
            !public.contains(&AUTH_SESSION_TTL_SECONDS),
            "a tenant-tier key that is not declared public must never reach the unauthenticated surface"
        );
        for def in keys::REGISTRY {
            assert!(
                def.public || !public.contains(&def.key),
                "{} reached the public view without being declared public",
                def.key
            );
        }
    }

    /// `tenant_view` is what a tenant admin sees, and it must never include a key they cannot set —
    /// listing a fleet-wide or operator key there would invite exactly the "just make this one
    /// per-tenant" pressure the tier boundary exists to resist.
    #[test]
    fn the_tenant_view_lists_only_tenant_writable_keys() {
        let effective = EffectiveConfig::new(Arc::new(ConfigSnapshot::default()), None);
        for (def, _, _) in effective.tenant_view() {
            assert_eq!(def.level, ConfigLevel::Tenant, "{} is not tenant-writable", def.key);
        }
    }
}
