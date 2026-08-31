//! Bootstraps the "platform" pieces every metap-based binary rebuilds by hand: Postgres pool,
//! the multi-tenant `Router` (with whichever `SecretStore` backend `AppConfig` selects),
//! `PermissionService` (Redis-backed policy cache if configured), and the JWT keypair. Found
//! copy-pasted near-identically in `metap-demo-crm/src/main.rs`, `metap-demo-jira/src/main.rs`,
//! and `templates/metap-app/src/main.rs` (2026-08-31,
//! `docs/features/08-metap-runtime-common-crate.md`).
//!
//! Deliberately its own crate, not part of `metap-runtime`: this needs `metap-control`/
//! `metap-permission`/`metap-cache`/`metap-infra`, and those already depend on `metap-runtime`
//! (for its HTTP client/bearer/CORS/error-shape/shutdown/middleware helpers) — folding this in
//! there would create a real dependency cycle (`metap-infra` -> `metap-runtime` ->
//! `metap-infra`). This crate sits at the same tier as the facade `metap` instead: depends on
//! every core crate it needs, nothing core depends back on it.
//!
//! ## Writing a custom (non-entity) backend on top of this
//!
//! Beyond declaring entities via `MetadataRegistry`, a real custom route/handler is built from
//! primitives that already exist and are already generic — nothing new needed here, just where
//! to find each one (`metap-lowcode-http`, in `../metap-lowcode`, is a full working example of
//! every piece below, not a hypothetical):
//! - **Custom router**: mount any `axum::Router<metap_http::AppState>` via
//!   `metap_http::build_router`'s `extra_routes` argument — that's how `metap-lowcode-http`/
//!   `metap-control-http` themselves are wired in by a downstream binary's own `main.rs`.
//! - **Tenant-scoped DB access**: `PlatformParts::router` (`metap_control::Router::pool_for`)
//!   resolves the right `PgPool` for the caller's tenant — never reach for a bare pool.
//! - **Permission-aware handlers**: extract `metap_http::auth::{AuthContext, AdminContext,
//!   PlatformAdminContext}` in a handler's signature — no separate middleware needed.
//! - **Publish an event**: `metap_infra::outbox::enqueue(executor, &event)` in the same
//!   transaction as the business write (the outbox pattern — never publish to `EventBus`
//!   directly from a handler).
//! - **Subscribe to events**: `metap_infra::EventBus::subscribe` — `notification-worker`
//!   (`crates/notification-worker`) is a full working example of a standalone consumer.
//!
//! What's deliberately NOT here, because it's genuinely different per binary, not boilerplate:
//! entity registration/reconciliation order, which optional `-http` crates to mount, gRPC/
//! notification-worker inline toggles, static file serving. Each binary's own `main.rs` still
//! owns those.

use std::sync::Arc;
use std::time::Duration;

use jsonwebtoken::DecodingKey;
use metap_control::{PostgresPolicyStore, PostgresTenantRegistry, RegistryCache, Router};
use metap_infra::AppConfig;
use metap_permission::PermissionService;
use sqlx::PgPool;

pub struct PlatformParts {
    pub pool: PgPool,
    pub router: Router,
    pub permissions: Arc<PermissionService>,
    pub decoding_key: DecodingKey,
    pub private_key_pem: String,
}

/// Connects to Postgres, builds the tenant `Router` (secret-store backend picked by
/// `metap_control::build_secret_store`'s own precedence order — GCP, then AWS, then Vault, then
/// `EnvStore`), builds `PermissionService` (Redis-backed policy cache if
/// `config.policy_cache_redis_url` is set, fully uncached otherwise), and reads the JWT keypair.
/// Does not build `metap_http::AppState` itself — the caller still owns its own
/// `MetadataRegistry` (entity registration is genuinely per-binary), so it passes these parts
/// into `AppState::new` alongside that.
pub async fn bootstrap_platform(config: &AppConfig) -> anyhow::Result<PlatformParts> {
    tracing::info!("connecting to postgres...");
    let pool = metap_infra::connect_db(&config.database_url).await?;

    let secret_store = metap_control::build_secret_store(config).await?;
    let tenant_registry = Arc::new(PostgresTenantRegistry::new(pool.clone()));
    let router = Router::new(pool.clone(), RegistryCache::new(tenant_registry), secret_store);

    let permissions = match &config.policy_cache_redis_url {
        Some(url) => {
            let ttl = Duration::from_secs(config.policy_cache_ttl_seconds);
            let cache = metap_cache::RedisCache::connect(url, ttl).await?;
            PermissionService::with_cache(
                Box::new(PostgresPolicyStore::new(router.clone())),
                Arc::new(cache) as Arc<dyn metap_cache::Cache>,
            )
        }
        None => PermissionService::new(Box::new(PostgresPolicyStore::new(router.clone()))),
    };

    let public_key_pem = std::fs::read(&config.auth_jwt_public_key_path)
        .map_err(|e| anyhow::anyhow!("failed to read {}: {e}", config.auth_jwt_public_key_path))?;
    let decoding_key = DecodingKey::from_rsa_pem(&public_key_pem)?;

    let private_key_pem = std::fs::read_to_string(&config.auth_jwt_private_key_path)
        .map_err(|e| anyhow::anyhow!("failed to read {}: {e}", config.auth_jwt_private_key_path))?;

    Ok(PlatformParts {
        pool,
        router,
        permissions: Arc::new(permissions),
        decoding_key,
        private_key_pem,
    })
}
