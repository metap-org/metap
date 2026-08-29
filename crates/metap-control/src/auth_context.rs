//! Turns a verified access token's claims into a `RequestContext` — the DB-touching half of
//! authentication that `metap-peripherals::decode_access_token` (the pure JWT-verify half)
//! deliberately can't do itself, since it needs a tenant-scoped `Router` (this crate) to open
//! the transaction `get_roles_for_user`/`fetch_context_attributes` run against. Lives here
//! rather than in `metap-peripherals` specifically to avoid a dependency cycle: this crate
//! already depends on `metap-peripherals`, so `metap-peripherals` can never depend back on it.
//!
//! Moved here from `crates/metap-http/src/cache.rs` (`ContextAttributesCache`) and
//! `crates/metap-http/src/auth.rs` (the role/context-attributes resolution logic previously
//! inlined in `AuthContext::from_request_parts`) so a future non-HTTP transport (a gRPC auth
//! interceptor) can call the exact same pipeline instead of reimplementing it — `metap-http`
//! keeps a `pub use` of `ContextAttributesCache` for source compatibility, since nothing about
//! its shape changed.

use std::future::Future;
use std::sync::Arc;
use std::time::Duration;

use metap_peripherals::{fetch_context_attributes, get_roles_for_user};
use metap_permission::RequestContext;
use moka::future::Cache;
use serde_json::{Map as JsonMap, Value};
use uuid::Uuid;

use crate::router::Router;

type CachedAttributes = Arc<Option<JsonMap<String, Value>>>;

/// Same shape as `metap-control::RegistryCache` (`moka::future::Cache`, TTL, `try_get_with` to
/// de-dupe concurrent misses for the same key). Unlike role lookup (always fresh, never cached —
/// `docs/architectures/06-runtime.md`), this **is** cached: caller attributes come from an
/// ordinary business record (an "employee", say), not a security-critical role assignment, and
/// the whole point of the `AUTH_CONTEXT_ENTITY` feature is to avoid a DB round-trip on every
/// authenticated request when nothing about the caller's org membership actually changes between
/// requests. A cache miss re-fetches; `invalidate` gives an operator an explicit way to clear a
/// stale entry immediately instead of waiting out the TTL.
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
    /// this module stays free of any entity-name knowledge, matching `RegistryCache`'s "cache
    /// wraps access, not the lookup" shape. `None` (no matching record) is cached too, so a user
    /// with no membership record doesn't re-query on every request either.
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

/// Builds a `RequestContext` for an already-verified token: live role lookup (never cached,
/// `get_roles_for_user`) plus the `AUTH_CONTEXT_ENTITY` opt-in best-effort attributes lookup
/// (cached via `context_attributes_cache`). Identical behavior to what
/// `crates/metap-http/src/auth.rs`'s `AuthContext::from_request_parts` used to inline: any
/// failure resolving roles propagates as an error (identity without roles is unusable), while a
/// failure resolving context attributes only logs and yields `None` (supplementary ABAC context,
/// never blocks auth).
pub async fn resolve_request_context(
    router: &Router,
    tenant_id: Uuid,
    user_id: Uuid,
    function_id: Option<String>,
    auth_context_entity: Option<&str>,
    context_attributes_cache: &ContextAttributesCache,
) -> anyhow::Result<RequestContext> {
    // Routed through `Router`, not a bare pool — a `DedicatedDb`-strategy tenant's `user_roles`
    // table lives only in that tenant's own database. `PLATFORM_TENANT_ID` (never a real
    // `control.tenants` row) takes `Router::begin`'s documented unregistered-tenant fallback
    // (`{Active, Schema("public")}`) — exactly where `users`/`user_roles` for that sentinel
    // actually live, so no special-casing is needed here.
    let mut tx = router.begin(tenant_id.into()).await?;
    let roles = get_roles_for_user(&mut *tx, tenant_id, user_id).await?;
    tx.commit().await?;

    let context_attributes = match auth_context_entity {
        Some(entity_name) => {
            let entity_name = entity_name.to_string();
            let router = router.clone();
            context_attributes_cache
                .get_with(tenant_id, user_id, move || async move {
                    let mut tx = router.begin(tenant_id.into()).await?;
                    let result = fetch_context_attributes(&mut *tx, tenant_id, &entity_name, user_id).await?;
                    tx.commit().await?;
                    Ok(result)
                })
                .await
                .unwrap_or_else(|err| {
                    tracing::warn!(%tenant_id, %user_id, error = %err, "failed to resolve context attributes");
                    None
                })
        }
        None => None,
    };

    Ok(RequestContext {
        tenant_id: tenant_id.to_string(),
        user_id: Some(user_id.to_string()),
        roles: Some(roles),
        function_id,
        context_attributes,
    })
}
