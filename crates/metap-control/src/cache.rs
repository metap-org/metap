//! Wraps a `TenantRegistry` with a short-TTL cache so `Router::begin` (called on every
//! tenant-scoped request) doesn't hit `control.tenants` per request — `control.tenants` changes
//! rarely (provisioning, suspend/promote), so a 30s staleness window is an acceptable tradeoff
//! against DB load.

use std::sync::Arc;
use std::time::Duration;

use moka::future::Cache;
use uuid::Uuid;

use crate::registry::TenantRegistry;
use crate::tenant::{TenantId, TenantRouting};

const TTL: Duration = Duration::from_secs(30);

#[derive(Clone)]
pub struct RegistryCache {
    registry: Arc<dyn TenantRegistry>,
    // `None` is cached too (unregistered tenant) — that lookup is exactly as expensive as a
    // registered one and just as stable for the TTL window.
    cache: Cache<Uuid, Arc<Option<TenantRouting>>>,
}

impl RegistryCache {
    pub fn new(registry: Arc<dyn TenantRegistry>) -> Self {
        Self {
            registry,
            cache: Cache::builder().time_to_live(TTL).build(),
        }
    }

    pub async fn get(&self, tenant: TenantId) -> anyhow::Result<Option<TenantRouting>> {
        let registry = self.registry.clone();
        let entry = self
            .cache
            // `try_get_with` de-dupes concurrent misses for the same key (thundering herd) and,
            // unlike `get_with`, does not poison the cache with an `Err` result.
            .try_get_with(tenant.0, async move { registry.get(tenant).await.map(Arc::new) })
            .await
            .map_err(|e| anyhow::anyhow!("tenant registry lookup failed for {tenant}: {e}"))?;
        Ok((*entry).clone())
    }
}
