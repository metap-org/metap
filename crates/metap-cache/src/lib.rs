//! A swappable cache — same shape as `metap-storage`'s `ObjectStore`/`metap-control`'s
//! `SecretStore`/`metap-infra`'s `EventBus`: one trait, callers hold `Arc<dyn Cache>`, never the
//! concrete type. Two impls today:
//! - `MokaCache` — in-process, matching what
//!   `metap-control::RegistryCache`/`Router::dedicated_pools`/`metap-http::ContextAttributesCache`
//!   already use (moka, not a new dependency). Cheapest, but each server instance warms its own
//!   copy — fine for today's single-instance topology, wrong once there's more than one.
//! - `RedisCache` — distributed, over any RESP-protocol server (Redis, DragonflyDB, Valkey, KeyDB
//!   are all wire-compatible; see `redis_cache.rs`'s doc comment for why DragonflyDB is the
//!   backend actually run in `docker-compose.yml`'s dev stack). Lets a horizontally scaled
//!   multi-instance deployment share one cache instead of each instance warming its own — closes
//!   the gap `docs/architectures/11-risks.md`/`07-deployment.md` both flag (load balancer,
//!   multiple instances, "not addressed yet"). Behind the `redis-backend` feature (off by
//!   default, 2026-09-02) — `Cache`/`MokaCache` have no Redis dependency at all, and most crates
//!   in the workspace only ever need the trait (via `metap-permission`), not this impl.
//!
//! A caller picks whichever `Arc<dyn Cache>` fits its deployment; nothing above this trait needs
//! to know or care which one is behind it.
//!
//! **Security-first, not caller discipline**: every method takes `tenant_id` as a mandatory
//! first parameter, same reasoning `ObjectStore` already established — a cache key collision
//! across tenants would leak one tenant's data into another's response. `MokaCache` folds
//! `tenant_id` into the real cache key internally; no call site constructs the raw key itself.
//!
//! **What this must never cache**: role/`user_roles` data. `crates/metap-http/src/auth.rs`'s
//! doc comment and `CLAUDE.md` both call out, independently, that roles are looked up fresh from
//! `user_roles` on *every* request, never cached on the token or anywhere else — a role
//! revocation must take effect on the very next request, not after a TTL. This is a load-bearing
//! security invariant, not an oversight to "optimize" — the identified real use for this crate is
//! `PermissionSnapshot`/policy-row caching (`metap-permission`), which — like
//! `ContextAttributesCache`'s own org-membership-attribute caching — is ordinary config data
//! that changes occasionally, not a security-critical role assignment, and gets a short TTL
//! (matching `ContextAttributesCache`'s 30s convention) plus explicit invalidation on write, not
//! an unbounded/permanent cache.

mod moka_cache;
#[cfg(feature = "redis-backend")]
mod redis_cache;

pub use moka_cache::MokaCache;
#[cfg(feature = "redis-backend")]
pub use redis_cache::RedisCache;

use async_trait::async_trait;
use bytes::Bytes;
use uuid::Uuid;

/// Lighter than `metap-storage::ObjectStore`'s key validation — a cache key never becomes a
/// URL/path the way an S3 object key can, and every key here is built by trusted internal code
/// (e.g. `"policies:{entity}"`), never derived directly from request input. Still checked, not
/// trusted blindly: non-empty, no control characters, bounded length — the same "don't assume
/// no caller ever misuses this" discipline the rest of this session applied to `table_name`/
/// schema names/object keys.
fn validate_key(key: &str) -> anyhow::Result<()> {
    anyhow::ensure!(!key.is_empty(), "cache key must not be empty");
    anyhow::ensure!(key.len() <= 256, "cache key must be at most 256 bytes");
    anyhow::ensure!(
        !key.chars().any(|c| c.is_control()),
        "cache key must not contain control characters"
    );
    Ok(())
}

#[async_trait]
pub trait Cache: Send + Sync {
    async fn get(&self, tenant_id: Uuid, key: &str) -> anyhow::Result<Option<Bytes>>;

    /// No `ttl` parameter here — a `Cache` instance has one fixed TTL, chosen when it's built
    /// (`MokaCache::new(ttl)`), same shape `ContextAttributesCache`/`RegistryCache` already use.
    /// One cache = one kind of data with one appropriate expiry (a policy-snapshot cache and a
    /// future different-purpose cache are two separate `Cache` instances, not one cache juggling
    /// several TTLs) — simpler than a per-call TTL, and there is still no "cache forever" call
    /// shape: every instance's entries expire on *some* bounded schedule, never indefinitely.
    async fn set(&self, tenant_id: Uuid, key: &str, value: Bytes) -> anyhow::Result<()>;

    /// Removing a key that isn't cached is not an error — same idempotent-delete reasoning
    /// `ObjectStore::delete` already uses.
    async fn invalidate(&self, tenant_id: Uuid, key: &str) -> anyhow::Result<()>;
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn validate_key_accepts_a_normal_key() {
        assert!(validate_key("policies:crm.customers").is_ok());
    }

    #[test]
    fn validate_key_rejects_empty_control_chars_and_overlong() {
        assert!(validate_key("").is_err());
        assert!(validate_key("a\0b").is_err());
        assert!(validate_key(&"a".repeat(257)).is_err());
    }
}
