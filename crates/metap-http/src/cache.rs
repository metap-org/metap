//! Caches `AuthContext`'s caller-attributes lookup (`docs/features/03-organization-identity.md`)
//! — same shape as `metap-control::RegistryCache` (`moka::future::Cache`, TTL, `try_get_with` to
//! de-dupe concurrent misses for the same key). Deliberately kept out of `metap-permission`:
//! this crate is the one with a `PgExecutor`/`Router` to actually query `records`, and the
//! caller-attributes feature is an HTTP-layer opt-in (`AUTH_CONTEXT_ENTITY`), not something
//! `metap-permission` needs to know exists.
//!
//! Unlike role lookup (`get_roles_for_user`, always fresh, never cached —
//! `docs/architectures/06-runtime.md`), this **is** cached: caller attributes come from an
//! ordinary business record (an "employee", say), not a security-critical role assignment, and
//! the whole point of this feature is to avoid a DB round-trip on every authenticated request
//! when nothing about the caller's org membership actually changes between requests. A cache
//! miss re-fetches; `invalidate` (wired to `POST /admin/users/{userId}/context/invalidate`)
//! gives an operator an explicit way to clear a stale entry immediately instead of waiting out
//! the TTL.

use std::future::Future;
use std::sync::Arc;
use std::time::Duration;

use moka::future::Cache;
use serde_json::{Map as JsonMap, Value};
use uuid::Uuid;

type CachedAttributes = Arc<Option<JsonMap<String, Value>>>;

#[derive(Clone)]
pub struct ContextAttributesCache {
    cache: Cache<(Uuid, Uuid), CachedAttributes>,
}

impl ContextAttributesCache {
    pub fn new(ttl: Duration) -> Self {
        Self {
            cache: Cache::builder().time_to_live(ttl).build(),
        }
    }

    /// `fetch` runs only on a cache miss — the actual `records` query, supplied by the caller so
    /// this module stays free of any `sqlx`/entity-name knowledge, matching `RegistryCache`'s
    /// "cache wraps access, not the lookup" shape. `None` (no matching record) is cached too, so
    /// a user with no membership record doesn't re-query on every request either.
    pub async fn get_with<F, Fut>(
        &self,
        tenant_id: Uuid,
        user_id: Uuid,
        fetch: F,
    ) -> anyhow::Result<Option<JsonMap<String, Value>>>
    where
        F: FnOnce() -> Fut,
        Fut: Future<Output = anyhow::Result<Option<JsonMap<String, Value>>>>,
    {
        let entry = self
            .cache
            .try_get_with((tenant_id, user_id), async move { fetch().await.map(Arc::new) })
            .await
            .map_err(|e| anyhow::anyhow!("context attributes lookup failed for {tenant_id}/{user_id}: {e}"))?;
        Ok((*entry).clone())
    }

    /// No-op if the key isn't cached — safe to call unconditionally, matching
    /// `POST /admin/users/{userId}/context/invalidate`'s "invalidate even if we're not sure
    /// there's anything to invalidate" usage. `moka::future::Cache::invalidate` is itself async
    /// (it synchronizes with the cache's internal eviction housekeeping), not fire-and-forget.
    pub async fn invalidate(&self, tenant_id: Uuid, user_id: Uuid) {
        self.cache.invalidate(&(tenant_id, user_id)).await;
    }
}

/// Caches `AuthContext`'s "does this tenant have Basic auth enabled" check
/// (`metap_auth::enabled_providers`) — same shape and reasoning as `ContextAttributesCache`
/// above, except this one matters more: unlike a Bearer JWT (minted once at login, verified
/// locally with no DB hit for the tenant-auth-config question at all), a Basic-authed request
/// carries no session and hits this check on *every single request*.
#[derive(Clone)]
pub struct TenantAuthCache {
    cache: Cache<Uuid, Arc<Vec<metap_auth::AuthProviderKind>>>,
}

impl TenantAuthCache {
    pub fn new(ttl: Duration) -> Self {
        Self {
            cache: Cache::builder().time_to_live(ttl).build(),
        }
    }

    pub async fn get_with<F, Fut>(&self, tenant_id: Uuid, fetch: F) -> anyhow::Result<Vec<metap_auth::AuthProviderKind>>
    where
        F: FnOnce() -> Fut,
        Fut: Future<Output = anyhow::Result<Vec<metap_auth::AuthProviderKind>>>,
    {
        let entry = self
            .cache
            .try_get_with(tenant_id, async move { fetch().await.map(Arc::new) })
            .await
            .map_err(|e| anyhow::anyhow!("tenant auth config lookup failed for {tenant_id}: {e}"))?;
        Ok((*entry).clone())
    }
}

/// Bridges `GET /auth/oidc/{tenant_id}/login`'s redirect to `GET /auth/oidc/{tenant_id}/callback`
/// — `metap-auth` has no HTTP-session concept of its own (`oidc.rs`'s doc comment), so this crate
/// is where the CSRF token / nonce / PKCE verifier a login attempt generates has to live until
/// the IdP redirects back with that same CSRF token as its `state` param. Not a `moka` TTL alone:
/// `take` removes the entry on first read (one-time use, same reason an OAuth `state`/nonce must
/// never be replayable) — TTL is only a backstop for an abandoned flow that never completes.
#[derive(Clone)]
pub struct OidcFlowCache {
    cache: Cache<String, Arc<OidcFlowEntry>>,
}

#[derive(Clone)]
pub struct OidcFlowEntry {
    pub tenant_id: Uuid,
    pub nonce: String,
    pub pkce_verifier: String,
}

impl OidcFlowCache {
    pub fn new(ttl: Duration) -> Self {
        Self {
            cache: Cache::builder().time_to_live(ttl).build(),
        }
    }

    pub async fn insert(&self, csrf_token: String, entry: OidcFlowEntry) {
        self.cache.insert(csrf_token, Arc::new(entry)).await;
    }

    pub async fn take(&self, csrf_token: &str) -> Option<Arc<OidcFlowEntry>> {
        let entry = self.cache.get(csrf_token).await;
        self.cache.invalidate(csrf_token).await;
        entry
    }
}
