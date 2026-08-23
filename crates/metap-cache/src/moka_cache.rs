//! `Cache` over `moka::future::Cache` — in-process, same library
//! `metap-control::RegistryCache`/`Router::dedicated_pools`/`metap-http::ContextAttributesCache`
//! already use. Fine for a single-instance deployment (today's actual topology); a horizontally
//! scaled deployment would swap in a `RedisCache` instead, no change to any caller (see this
//! crate's top doc comment).

use async_trait::async_trait;
use bytes::Bytes;
use std::time::Duration;
use uuid::Uuid;

use crate::{validate_key, Cache};

pub struct MokaCache {
    inner: moka::future::Cache<String, Bytes>,
}

impl MokaCache {
    pub fn new(ttl: Duration) -> Self {
        Self {
            inner: moka::future::Cache::builder().time_to_live(ttl).build(),
        }
    }

    /// The real cache key for `(tenant_id, key)` — every trait method goes through this, never
    /// uses `key` directly, so tenant scoping can't be bypassed by a call site that forgets to
    /// prefix it itself (see this crate's top doc comment).
    fn scoped_key(tenant_id: Uuid, key: &str) -> anyhow::Result<String> {
        validate_key(key)?;
        Ok(format!("{tenant_id}:{key}"))
    }
}

#[async_trait]
impl Cache for MokaCache {
    async fn get(&self, tenant_id: Uuid, key: &str) -> anyhow::Result<Option<Bytes>> {
        let key = Self::scoped_key(tenant_id, key)?;
        Ok(self.inner.get(&key).await)
    }

    async fn set(&self, tenant_id: Uuid, key: &str, value: Bytes) -> anyhow::Result<()> {
        let key = Self::scoped_key(tenant_id, key)?;
        self.inner.insert(key, value).await;
        Ok(())
    }

    async fn invalidate(&self, tenant_id: Uuid, key: &str) -> anyhow::Result<()> {
        let key = Self::scoped_key(tenant_id, key)?;
        self.inner.invalidate(&key).await;
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[tokio::test]
    async fn get_after_set_returns_the_value() {
        let cache = MokaCache::new(Duration::from_secs(30));
        let tenant = Uuid::new_v4();
        cache.set(tenant, "k", Bytes::from_static(b"v")).await.unwrap();
        assert_eq!(cache.get(tenant, "k").await.unwrap(), Some(Bytes::from_static(b"v")));
    }

    #[tokio::test]
    async fn get_on_a_missing_key_is_ok_none() {
        let cache = MokaCache::new(Duration::from_secs(30));
        assert_eq!(cache.get(Uuid::new_v4(), "missing").await.unwrap(), None);
    }

    #[tokio::test]
    async fn same_key_different_tenants_never_collide() {
        let cache = MokaCache::new(Duration::from_secs(30));
        let tenant_a = Uuid::new_v4();
        let tenant_b = Uuid::new_v4();
        cache
            .set(tenant_a, "k", Bytes::from_static(b"a's value"))
            .await
            .unwrap();
        cache
            .set(tenant_b, "k", Bytes::from_static(b"b's value"))
            .await
            .unwrap();
        assert_eq!(
            cache.get(tenant_a, "k").await.unwrap(),
            Some(Bytes::from_static(b"a's value"))
        );
        assert_eq!(
            cache.get(tenant_b, "k").await.unwrap(),
            Some(Bytes::from_static(b"b's value"))
        );
    }

    #[tokio::test]
    async fn invalidate_removes_only_that_tenants_entry() {
        let cache = MokaCache::new(Duration::from_secs(30));
        let tenant_a = Uuid::new_v4();
        let tenant_b = Uuid::new_v4();
        cache.set(tenant_a, "k", Bytes::from_static(b"a")).await.unwrap();
        cache.set(tenant_b, "k", Bytes::from_static(b"b")).await.unwrap();
        cache.invalidate(tenant_a, "k").await.unwrap();
        assert_eq!(cache.get(tenant_a, "k").await.unwrap(), None);
        assert_eq!(cache.get(tenant_b, "k").await.unwrap(), Some(Bytes::from_static(b"b")));
    }

    #[tokio::test]
    async fn entries_expire_after_ttl() {
        let cache = MokaCache::new(Duration::from_millis(50));
        let tenant = Uuid::new_v4();
        cache.set(tenant, "k", Bytes::from_static(b"v")).await.unwrap();
        assert_eq!(cache.get(tenant, "k").await.unwrap(), Some(Bytes::from_static(b"v")));
        tokio::time::sleep(Duration::from_millis(150)).await;
        assert_eq!(cache.get(tenant, "k").await.unwrap(), None, "entry must have expired");
    }
}
