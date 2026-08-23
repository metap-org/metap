//! `Cache` over Redis (or any RESP-protocol-compatible server — DragonflyDB, Valkey, KeyDB all
//! speak the same wire protocol, so this one impl works unmodified against any of them; only the
//! connection URL's port/host changes). This is the distributed counterpart to `MokaCache`: a
//! horizontally scaled deployment (multiple `crm-server`/`jira-server` instances behind a load
//! balancer — `docs/architectures/07-deployment.md`'s acknowledged, previously-unaddressed gap)
//! shares one cache here instead of each instance warming its own in-process copy, so a policy
//! write on instance A is visible to instance B immediately after the TTL/invalidation logic
//! runs, not only after A's own moka entry happens to expire.
//!
//! **Why Redis-protocol instead of a DragonflyDB-specific client**: DragonflyDB (and Valkey)
//! are drop-in RESP replacements for Redis — same wire protocol, same client libraries, same
//! `SET`/`GET`/`DEL`/`EXPIRE` semantics used here. Writing against the `redis` crate rather than
//! a vendor-specific SDK means swapping the actual server (Redis ⇄ DragonflyDB ⇄ Valkey) is a
//! deployment/connection-string change, zero code change — the same "interface, not vendor lock"
//! principle already applied to `metap-storage::ObjectStore` (SeaweedFS today, S3-API-compatible
//! swap later). DragonflyDB is the backend actually run in `docker-compose.yml` for local dev
//! (multi-threaded, single-node throughput several times Redis's in published benchmarks,
//! BSL-licensed — acceptable for self-hosted single-node use; Redis itself moved off BSD to
//! SSPL/RSALv2/AGPLv3 in 2024-2025, so neither option is the old permissively-licensed Redis
//! anymore) — but nothing in this file assumes DragonflyDB specifically, so a production
//! deployment can point `REDIS_URL` at real Redis or Valkey instead without touching this crate.
//!
//! **Security-first, same as `MokaCache`**: `tenant_id` is folded into the real Redis key inside
//! `scoped_key`, never exposed as a raw key a caller could construct wrong.

use async_trait::async_trait;
use bytes::Bytes;
use redis::AsyncCommands;
use std::time::Duration;
use uuid::Uuid;

use crate::{validate_key, Cache};

pub struct RedisCache {
    conn: redis::aio::ConnectionManager,
    ttl: Duration,
}

impl RedisCache {
    /// `url` is a standard `redis://` connection string — works unchanged against Redis,
    /// DragonflyDB, or Valkey (see this module's doc comment). `ConnectionManager` multiplexes
    /// one real connection and auto-reconnects on failure, so a single `RedisCache` is meant to
    /// be built once at boot and shared behind `Arc<dyn Cache>`, same as `MokaCache`.
    pub async fn connect(url: &str, ttl: Duration) -> anyhow::Result<Self> {
        let client = redis::Client::open(url)?;
        let conn = client.get_connection_manager().await?;
        Ok(Self { conn, ttl })
    }

    fn scoped_key(tenant_id: Uuid, key: &str) -> anyhow::Result<String> {
        validate_key(key)?;
        Ok(format!("metap-cache:{tenant_id}:{key}"))
    }
}

#[async_trait]
impl Cache for RedisCache {
    async fn get(&self, tenant_id: Uuid, key: &str) -> anyhow::Result<Option<Bytes>> {
        let key = Self::scoped_key(tenant_id, key)?;
        let mut conn = self.conn.clone();
        let value: Option<Vec<u8>> = conn.get(&key).await?;
        Ok(value.map(Bytes::from))
    }

    async fn set(&self, tenant_id: Uuid, key: &str, value: Bytes) -> anyhow::Result<()> {
        let key = Self::scoped_key(tenant_id, key)?;
        let mut conn = self.conn.clone();
        let seconds = self.ttl.as_secs().max(1);
        let _: () = conn.set_ex(&key, value.to_vec(), seconds).await?;
        Ok(())
    }

    async fn invalidate(&self, tenant_id: Uuid, key: &str) -> anyhow::Result<()> {
        let key = Self::scoped_key(tenant_id, key)?;
        let mut conn = self.conn.clone();
        let _: () = conn.del(&key).await?;
        Ok(())
    }
}

/// Runs only against a real Redis-protocol server (`REDIS_URL`, defaults to the DragonflyDB
/// dev-compose service) — same `#[ignore]`d e2e convention `metap-storage`'s SeaweedFS tests use.
#[cfg(test)]
mod tests {
    use super::*;

    async fn test_cache(ttl: Duration) -> RedisCache {
        let url = std::env::var("REDIS_URL").unwrap_or_else(|_| "redis://127.0.0.1:6379".to_string());
        RedisCache::connect(&url, ttl)
            .await
            .expect("connect to Redis/DragonflyDB")
    }

    #[tokio::test]
    #[ignore]
    async fn put_get_delete_round_trip_against_real_redis() {
        let cache = test_cache(Duration::from_secs(30)).await;
        let tenant = Uuid::new_v4();
        cache.set(tenant, "k", Bytes::from_static(b"v")).await.unwrap();
        assert_eq!(cache.get(tenant, "k").await.unwrap(), Some(Bytes::from_static(b"v")));
        cache.invalidate(tenant, "k").await.unwrap();
        assert_eq!(cache.get(tenant, "k").await.unwrap(), None);
    }

    #[tokio::test]
    #[ignore]
    async fn same_key_different_tenants_never_collide() {
        let cache = test_cache(Duration::from_secs(30)).await;
        let tenant_a = Uuid::new_v4();
        let tenant_b = Uuid::new_v4();
        cache.set(tenant_a, "k", Bytes::from_static(b"a")).await.unwrap();
        cache.set(tenant_b, "k", Bytes::from_static(b"b")).await.unwrap();
        assert_eq!(cache.get(tenant_a, "k").await.unwrap(), Some(Bytes::from_static(b"a")));
        assert_eq!(cache.get(tenant_b, "k").await.unwrap(), Some(Bytes::from_static(b"b")));
        cache.invalidate(tenant_a, "k").await.unwrap();
        cache.invalidate(tenant_b, "k").await.unwrap();
    }

    #[tokio::test]
    #[ignore]
    async fn entries_expire_after_ttl() {
        let cache = test_cache(Duration::from_secs(1)).await;
        let tenant = Uuid::new_v4();
        cache.set(tenant, "k", Bytes::from_static(b"v")).await.unwrap();
        assert_eq!(cache.get(tenant, "k").await.unwrap(), Some(Bytes::from_static(b"v")));
        tokio::time::sleep(Duration::from_millis(1500)).await;
        assert_eq!(cache.get(tenant, "k").await.unwrap(), None, "entry must have expired");
    }
}
