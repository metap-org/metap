//! `ContextAttributesCache` moved to `metap-control::auth_context` (2026-08-29) so
//! `metap_control::resolve_request_context` — the DB-touching half of turning a verified token
//! into a `RequestContext`, needed by any future non-HTTP transport too — can use it without
//! this crate creating a dependency cycle back onto `metap-http`. Re-exported here under its
//! original name/path (`metap_http::cache::ContextAttributesCache`) purely for source
//! compatibility with existing call sites (`apps/crm-server/src/main.rs`, this crate's own
//! tests) — nothing about its shape or behavior changed.
pub use metap_control::ContextAttributesCache;

use std::future::Future;
use std::sync::Arc;
use std::time::Duration;

use moka::future::Cache;
use uuid::Uuid;

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
