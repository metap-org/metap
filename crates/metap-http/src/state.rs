use std::sync::Arc;
use std::time::Duration;

use arc_swap::ArcSwap;
use jsonwebtoken::DecodingKey;
use metap_control::Router;
use metap_crud::CrudService;
use metap_metadata::MetadataRegistry;
use metap_permission::PermissionService;
use sqlx::PgPool;

use crate::cache::ContextAttributesCache;

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
    /// Code-authored entities only (`apps/crm-server/src/entities/*.rs`), fixed after boot —
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
    /// Opt-in caller-attributes entity name (`AUTH_CONTEXT_ENTITY`,
    /// `docs/features/03-organization-identity.md`) — `None` by default (set by `new`), the
    /// composition root (`apps/crm-server/src/main.rs`) assigns this directly after
    /// construction (every `AppState` field is `pub`) rather than threading two more
    /// constructor parameters through every call site, most of which don't use this feature.
    pub auth_context_entity: Option<Arc<str>>,
    /// Always constructed (cheap — an empty `moka` cache costs nothing until used), even when
    /// `auth_context_entity` is `None`, so `AuthContext` never has to branch on "does the cache
    /// exist" — only on "is the feature configured". `new` builds it with the default TTL (30s,
    /// matching `metap-control::RegistryCache`); the composition root rebuilds it with a
    /// configured TTL if `AUTH_CONTEXT_CACHE_TTL_SECONDS` overrides the default.
    pub context_attributes_cache: ContextAttributesCache,
}

impl AppState {
    /// `router` is built by the caller (`apps/crm-server/src/main.rs`), not here — unlike
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
        Self {
            pool,
            router,
            metadata_base,
            metadata,
            permissions,
            crud,
            jwt_decoding_key: Arc::new(jwt_decoding_key),
            jwt_encoding_key_pem: Arc::from(jwt_encoding_key_pem),
            auth_context_entity: None,
            context_attributes_cache: ContextAttributesCache::new(Duration::from_secs(30)),
        }
    }
}
