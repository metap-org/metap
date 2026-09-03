use std::collections::HashMap;
use std::sync::Arc;
use std::time::Duration;

use arc_swap::ArcSwap;
use jsonwebtoken::DecodingKey;
use metap_control::Router;
use metap_crud::CrudService;
use metap_metadata::MetadataRegistry;
use metap_permission::PermissionService;
use sqlx::PgPool;

use crate::cache::{ContextAttributesCache, OidcFlowCache, TenantAuthCache};
use crate::metrics::{process_collector, prometheus_handle};

#[derive(Clone)]
pub struct AppState {
    pub pool: PgPool,
    /// The multi-tenant seam (`docs/multi-tenant-platform-design.md` §2.2) — every tenant-scoped
    /// query goes through `router.begin(tenant)`, never `pool` directly, so a `DedicatedDb`-
    /// strategy tenant's data (which may live in a completely different physical database) is
    /// reached correctly. `crud` already holds a private clone of this same `Router` (see
    /// `CrudService::new` below); this field exists so callers *outside* `CrudService` — the
    /// `AuthContext`/`AdminContext` extractors (`crate::auth`) and `/auth/login`,
    /// `/admin/users*` handlers — can reach it too, closing the gap `docs/roadmap.md` Phase 16
    /// tracked ("role lookup và `PostgresPolicyStore` vẫn dùng `AppState.pool` trực tiếp,
    /// không qua Router").
    pub router: Router,
    /// Code-authored entities only (`../metap-demo-crm/src/entities/*.rs`), fixed after boot —
    /// never touched by a DB-authored publish/rollback. Used to reject a DB-authored draft
    /// whose name collides with a code-authored entity (`docs/roadmap.md` Phase 11 / Phase A
    /// sub-project 3) before it's ever merged into `metadata`.
    pub metadata_base: Arc<MetadataRegistry>,
    /// The live, request-serving registry: `metadata_base` merged with every currently
    /// published DB-authored entity (`metap_lowcode`). An `ArcSwap`, not a plain
    /// `Arc<MetadataRegistry>`, so a publish/rollback can swap in a freshly merged registry
    /// while the server keeps running — no restart (Phase A sub-project 2). See
    /// `metap-lowcode-http`'s `apply_registry` for the only place that calls `.store()`.
    pub metadata: Arc<ArcSwap<MetadataRegistry>>,
    pub permissions: Arc<PermissionService>,
    pub crud: Arc<CrudService>,
    pub jwt_decoding_key: Arc<DecodingKey>,
    /// Raw PEM, not a pre-parsed `jsonwebtoken::EncodingKey` — `POST /auth/login` mints
    /// rarely enough that re-parsing per request is not worth holding a second key type in
    /// state for. See `metap_peripherals::mint_jwt`, the only thing that reads this.
    pub jwt_encoding_key_pem: Arc<str>,
    /// `None` unless the host binary opts in (e.g. `../metap-demo-jira`, `.env`'s `S3_BUCKET`) —
    /// backs `crate::routes::attachments`' generic `/api/{entity}/{id}/attachments*` routes,
    /// always registered in `build_router` regardless of whether a given host actually
    /// configures storage (a request against an app that never set this just gets a 503, the
    /// same shape any other unconfigured optional feature degrades to here).
    pub object_store: Option<Arc<dyn metap_storage::ObjectStore>>,
    /// Entity name -> dedicated attachments table name, empty by default (every entity uses the
    /// shared `attachments` table, `crates/migrations/0021_attachments.sql`). An entity expecting
    /// heavy attachment volume can get its own table instead
    /// (`metap_attachments::ensure_dedicated_table`, called once at boot by the host binary,
    /// same pattern `reconcile()` already follows per entity) — this map is just how
    /// `routes::attachments` learns which table name to use for a given `:entity` path segment.
    pub attachment_tables: Arc<HashMap<String, String>>,
    /// Opt-in caller-attributes entity name (`AUTH_CONTEXT_ENTITY`,
    /// `docs/features/03-organization-identity.md`) — `None` by default (set by `new`), the
    /// composition root (`../metap-demo-crm/src/main.rs`) assigns this directly after
    /// construction (every `AppState` field is `pub`) rather than threading two more
    /// constructor parameters through every call site, most of which don't use this feature.
    pub auth_context_entity: Option<Arc<str>>,
    /// Always constructed (cheap — an empty `moka` cache costs nothing until used), even when
    /// `auth_context_entity` is `None`, so `AuthContext` never has to branch on "does the cache
    /// exist" — only on "is the feature configured". `new` builds it with the default TTL (30s,
    /// matching `metap-control::RegistryCache`); the composition root rebuilds it with a
    /// configured TTL if `AUTH_CONTEXT_CACHE_TTL_SECONDS` overrides the default.
    pub context_attributes_cache: ContextAttributesCache,
    /// Backs the `Authorization: Basic` branch of `AuthContext` (`crate::auth`) — see
    /// `TenantAuthCache`'s doc comment for why this one is cached more aggressively than a
    /// one-off login check would need.
    pub tenant_auth_cache: TenantAuthCache,
    /// Backs the OIDC redirect/callback pair (`crate::routes::auth`) — 10 minutes is generous
    /// enough for a real login (IdP page load + credential entry) while bounding how long an
    /// abandoned flow's PKCE verifier sits in memory.
    pub oidc_flow_cache: OidcFlowCache,
    /// `GET /metrics` (`crate::routes::metrics`, `docs/local-benchmarking.md`) — HTTP
    /// request-level metrics (count/duration/in-flight, per route) via `axum-prometheus`.
    /// `prometheus_handle()` installs the global `metrics` recorder exactly once per process
    /// (guarded by a `OnceLock` — `PrometheusMetricLayer::pair()` panics if called a second
    /// time, which every e2e test building its own `AppState` would otherwise trigger) and
    /// returns a cheap-to-clone handle either way.
    /// Fleet-wide tunables that used to be literals in this crate's source — the GraphQL
    /// depth/complexity pair (audit 04 A#7), the rate limit, and the session TTL
    /// (`docs/features/18-config-tiers-db-backed.md`). Backs `crate::routes::platform_config`.
    ///
    /// **Starts unloaded**, reading every key back as the same default the code used to hard-code:
    /// `new` is synchronous and reading the table is not. A host binary should
    /// `state.config.reload().await` once at boot *before* `build_router`, which is where the rate
    /// limit is read. Forgetting that is safe rather than broken — the platform simply behaves as
    /// it did before this table existed.
    pub config: Arc<metap_config::ConfigStore>,
    /// Backs the unauthenticated `GET /public/config` (`crate::routes::tenant_config`) — the one
    /// request path that has to turn a `Host` header into a tenant before any token exists. Cached
    /// because that endpoint is hit by every anonymous login-page load and is reachable by anyone
    /// who can reach the deployment; see `TenantHostnameCache`'s doc comment.
    pub tenant_hostname_cache: crate::cache::TenantHostnameCache,
    pub metrics_handle: axum_prometheus::metrics_exporter_prometheus::PrometheusHandle,
    /// Process-level resource metrics (CPU/RSS/open fds/threads) — `docs/local-benchmarking.md`.
    /// `.collect()` (called by the `/metrics` handler on every scrape, not on a background
    /// timer) refreshes the values read from `/proc` just before rendering.
    pub process_collector: metrics_process::Collector,
    /// OpenAPI path fragments contributed by optional platform capabilities this crate has zero
    /// dependency on (`metap-lowcode-http`, `metap-control-http` — same "extra_routes" boundary
    /// this file's doc comment already draws for the axum routes themselves). Empty by default;
    /// the composition root (`../metap-demo-crm/src/main.rs`) assigns it after construction, same
    /// pattern as `object_store`/`attachment_tables`/`auth_context_entity` above. Merged into
    /// `GET /metadata/openapi.json`'s `paths` alongside this crate's own static routes
    /// (`crate::openapi_paths::static_paths`) and the per-entity dynamic ones
    /// (`metap_metadata::generate_openapi_document`) — see `routes::metadata::openapi_json`.
    pub extra_openapi_paths: Arc<serde_json::Map<String, serde_json::Value>>,
    /// The `Secure` attribute on both cookies `crate::cookies`/`routes::auth` issue — defaults to
    /// `true` (the correct value for any real deployment, served over HTTPS) so no existing
    /// caller of `AppState::new` picks up an insecure default just by rebuilding. A local dev
    /// binary serving plain `http://localhost` needs to flip this to `false` explicitly
    /// (`state.cookie_secure = false;` — every field here is `pub`, same pattern as
    /// `object_store`/`auth_context_entity` above) since most browsers reject a `Secure` cookie
    /// outside `localhost`/HTTPS outright, though Chrome/Firefox both special-case plain
    /// `localhost` as a trustworthy origin already, so this is often not needed even then.
    pub cookie_secure: bool,
}

impl AppState {
    /// `router` is built by the caller (`../metap-demo-crm/src/main.rs`), not here — unlike
    /// before Phase 16's role-lookup/`PostgresPolicyStore` fix (2026-08-20), the composition
    /// root now needs the same `Router` instance for two things built *before* `AppState::new`
    /// runs (`PostgresPolicyStore::new(router.clone())`, wrapped into the `permissions` param
    /// below) as well as for this state — building it internally here, as it used to, would
    /// mean two different `Router`s (each with their own `RegistryCache`, defeating the point
    /// of sharing the tenant-lookup cache across every request path).
    pub fn new(
        pool: PgPool,
        metadata_base: Arc<MetadataRegistry>,
        metadata: Arc<ArcSwap<MetadataRegistry>>,
        permissions: Arc<PermissionService>,
        jwt_decoding_key: DecodingKey,
        jwt_encoding_key_pem: String,
        router: Router,
    ) -> Self {
        let crud = Arc::new(CrudService::new(router.clone(), metadata.clone(), permissions.clone()));
        let config = Arc::new(metap_config::ConfigStore::unloaded(pool.clone()));
        let tenant_hostname_cache = crate::cache::TenantHostnameCache::new(Duration::from_secs(60));
        Self {
            pool,
            config,
            router,
            metadata_base,
            metadata,
            permissions,
            crud,
            jwt_decoding_key: Arc::new(jwt_decoding_key),
            jwt_encoding_key_pem: Arc::from(jwt_encoding_key_pem),
            object_store: None,
            attachment_tables: Arc::new(HashMap::new()),
            auth_context_entity: None,
            context_attributes_cache: ContextAttributesCache::new(Duration::from_secs(30)),
            tenant_auth_cache: TenantAuthCache::new(Duration::from_secs(30)),
            oidc_flow_cache: OidcFlowCache::new(Duration::from_secs(600)),
            tenant_hostname_cache,
            metrics_handle: prometheus_handle(),
            process_collector: process_collector(),
            extra_openapi_paths: Arc::new(serde_json::Map::new()),
            cookie_secure: true,
        }
    }

    /// One tenant's effective configuration — its own `tenant_configs` overrides layered over the
    /// fleet snapshot (`metap_config::EffectiveConfig`).
    ///
    /// Cache-first, and the ordering is the point: a cached tenant costs no database work at all,
    /// not even the `router.begin` round trip that reaching `tenant_configs` would otherwise
    /// require. Only a miss opens a transaction.
    ///
    /// A lookup failure degrades to the fleet-wide values rather than propagating. Every caller is
    /// asking for a tunable that has a working default, and failing a login (or a login *page*)
    /// because a config row could not be read would turn an optional feature into a hard
    /// dependency.
    pub async fn effective_config(&self, tenant_id: uuid::Uuid) -> metap_config::EffectiveConfig {
        if let Some(cached) = self.config.cached_tenant(tenant_id).await {
            return self.config.effective(Some(cached));
        }
        let loaded = async {
            let mut tx = self.router.begin(tenant_id.into()).await.ok()?;
            let snapshot = self.config.load_tenant(&mut *tx, tenant_id).await.ok()?;
            let _ = tx.commit().await;
            Some(snapshot)
        }
        .await;
        if loaded.is_none() {
            tracing::warn!(%tenant_id, "tenant config lookup failed; using fleet-wide values");
        }
        self.config.effective(loaded)
    }
}
