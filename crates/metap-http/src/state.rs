use std::sync::Arc;

use arc_swap::ArcSwap;
use jsonwebtoken::DecodingKey;
use metap_control::{PostgresTenantRegistry, RegistryCache, Router, SecretStore};
use metap_crud::CrudService;
use metap_metadata::MetadataRegistry;
use metap_permission::PermissionService;
use sqlx::PgPool;

#[derive(Clone)]
pub struct AppState {
    pub pool: PgPool,
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
}

impl AppState {
    pub fn new(
        pool: PgPool,
        metadata_base: Arc<MetadataRegistry>,
        metadata: Arc<ArcSwap<MetadataRegistry>>,
        permissions: Arc<PermissionService>,
        jwt_decoding_key: DecodingKey,
        jwt_encoding_key_pem: String,
        secret_store: Arc<dyn SecretStore>,
    ) -> Self {
        // `Router` (`metap-control`, `docs/multi-tenant-platform-design.md` §2.2) is built here,
        // not passed in — same pattern `CrudService` itself already follows below — so
        // `apps/crm-server/src/main.rs` doesn't need to know about it. It wraps the same
        // `pool.clone()`, not a second physical pool. `secret_store` (which `SecretStore` impl
        // — `EnvStore` or `VaultStore`, Phase 16 Giai đoạn 4) is the caller's call, not this
        // constructor's — same "wiring inline at the composition root" pattern as everything
        // else in `apps/crm-server/src/main.rs`.
        let tenant_registry = Arc::new(PostgresTenantRegistry::new(pool.clone()));
        let router = Router::new(pool.clone(), RegistryCache::new(tenant_registry), secret_store);
        let crud = Arc::new(CrudService::new(router, metadata.clone(), permissions.clone()));
        Self {
            pool,
            metadata_base,
            metadata,
            permissions,
            crud,
            jwt_decoding_key: Arc::new(jwt_decoding_key),
            jwt_encoding_key_pem: Arc::from(jwt_encoding_key_pem),
        }
    }
}
