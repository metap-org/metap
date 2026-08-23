//! `apps/jira-server` — a second demo/proof app (`apps/crm-server` is the first), built
//! specifically to wire `metap_reconciler::reconcile()` into a real boot sequence for the first
//! time (`docs/roadmap.md`'s jira-server "bước 6"): `jira.projects`/`jira.issues` both use a
//! dedicated table (`table_name != "records"`, see `entities/`), reconciled here before the
//! server starts serving. Everything else mirrors `apps/crm-server/src/main.rs`'s boot sequence
//! as closely as possible, minus what this PoC doesn't need: no DB-authored (low-code) entity
//! merge, no `lowcode_http`/`control_http` router, no static frontend, no inline worker.
//!
//! **`JIRA_TENANT_ID` must be a properly provisioned `DedicatedDb` tenant, not the platform's
//! own dev/admin tenant.** An earlier version of this file reconciled straight against
//! `config.database_url` (the platform's own database) for a hardcoded fixed dev tenant id — the
//! same one `crm-server`'s dev tooling uses — which conflated "the low-code platform's own
//! sandbox DB" with "a customer's isolated tenant DB" (real feedback, not a hypothetical: if a
//! customer subscribes to build their own custom Jira on this platform, `my-jira` should be a
//! `control.tenants` row with its own database — `docs/multi-tenant-platform-design.md` §2's
//! DB-per-paying-tenant model — not another app quietly reusing the platform's shared DB and
//! dev tenant id). Fixed by provisioning a real tenant first
//! (`dev-tools provision-tenant <id> dedicated_db <dsnSecretRef> <dedicatedDatabaseUrl> <email>
//! <password>`, see this crate's README/`.env.example`) and resolving its actual pool through
//! `Router::pool_for` (added specifically for this — `Router` previously only exposed
//! `begin()`, transaction-scoped, no good for `reconcile()`'s non-transactional DDL) instead of
//! the platform's own connection.
//!
//! `pool`/`config.database_url` below is still the platform's own control-plane database
//! (`control.tenants`, and — inheriting the same pattern `crm-server` already has —
//! `AppState.pool`'s `/auth/login`, `/preferences`, cron routes, which are not yet
//! `Router`-resolved per tenant anywhere in this codebase; a `DedicatedDb` tenant's own
//! `users` row, created on its dedicated pool during provisioning, is unreachable through
//! those specific routes today — a pre-existing gap in `crm-server` too, not introduced here,
//! out of scope for this fix. Use `dev-tools mint-token` — reads only a JWT keypair, no DB
//! lookup — to get a working token for this tenant instead of `/auth/login` until that's fixed).
//!
//! **The reconcile call below is still a direct, single-tenant call at boot — not the
//! multi-tenant orchestrator** (`crates/metap-reconciler/src/orchestrator.rs`'s `claim_due`/wave
//! rollout, which no binary runs yet). A tenant registered *after* this process starts would
//! never get `jira_projects`/`jira_issues` reconciled for it automatically.

mod entities;

use entities::{
    comment_entity::comment_entity, issue_entity::issue_entity, project_entity::project_entity,
    sprint_entity::sprint_entity,
};

use std::sync::Arc;

use arc_swap::ArcSwap;
use jsonwebtoken::DecodingKey;
use metap::prelude::*;
use uuid::Uuid;

#[tokio::main]
async fn main() -> anyhow::Result<()> {
    metap::infra::init_tracing();
    let config = load_config()?;

    tracing::info!("connecting to postgres...");
    let pool = connect_db(&config.database_url).await?;

    let mut registry = MetadataRegistry::new();
    registry.register(project_entity())?;
    registry.register(sprint_entity())?;
    registry.register(issue_entity())?;
    registry.register(comment_entity())?;
    registry.validate_references()?;
    let metadata_base = Arc::new(registry);
    let metadata = Arc::new(ArcSwap::new(Arc::new((*metadata_base).clone())));

    // `Router` built before reconcile() runs — reconcile needs it to resolve the tenant's own
    // pool, not the platform's `pool` above (see this file's top doc comment).
    let tenant_registry = Arc::new(metap::control::PostgresTenantRegistry::new(pool.clone()));
    let router = metap::control::Router::new(
        pool.clone(),
        metap::control::RegistryCache::new(tenant_registry),
        Arc::new(metap::control::EnvStore),
    );

    // Must already be a provisioned `control.tenants` row (`dev-tools provision-tenant`) —
    // there is no fallback to the platform's shared DB here the way an unregistered tenant id
    // would silently get one from `Router` (that fallback exists for the pre-provisioning dev
    // flow, not for a boot sequence that's about to reconcile real tables into it).
    let jira_tenant_id: Uuid = std::env::var("JIRA_TENANT_ID")
        .map_err(|_| anyhow::anyhow!("JIRA_TENANT_ID is required — provision a tenant first (see .env.example)"))?
        .parse()?;
    let tenant_pool = router.pool_for(jira_tenant_id.into()).await.map_err(|e| {
        anyhow::anyhow!(
            "failed to resolve JIRA_TENANT_ID={jira_tenant_id}'s pool — is it provisioned \
             (dev-tools provision-tenant) and is its dsn_secret_ref env var set? {e}"
        )
    })?;
    // Order is load-bearing: each entity's Reference fields build a real FK straight into its
    // target's table at DDL time (`compile()`, see `sprint_entity.rs`'s doc comment), so a
    // referenced entity must be reconciled — its table must physically exist — before the
    // entity that references it. project -> sprint (references project) -> issue (references
    // project + sprint) -> comment (references issue).
    for entity in [project_entity(), sprint_entity(), issue_entity(), comment_entity()] {
        let outcome = metap_reconciler::reconcile(&tenant_pool, jira_tenant_id, &entity, &[]).await?;
        tracing::info!(
            entity = entity.name,
            table = outcome.table,
            ops_applied = outcome.ops_applied,
            "reconciled dedicated table"
        );
    }

    // See `apps/crm-server/src/main.rs`'s identical block for why this is opt-in/Redis-backed.
    let permissions = match &config.policy_cache_redis_url {
        Some(url) => {
            let ttl = std::time::Duration::from_secs(config.policy_cache_ttl_seconds);
            let cache = metap::cache::RedisCache::connect(url, ttl).await?;
            PermissionService::with_cache(
                Box::new(PostgresPolicyStore::new(router.clone())),
                Arc::new(cache) as Arc<dyn metap::cache::Cache>,
            )
        }
        None => PermissionService::new(Box::new(PostgresPolicyStore::new(router.clone()))),
    };

    let public_key_pem = std::fs::read(&config.auth_jwt_public_key_path)
        .map_err(|e| anyhow::anyhow!("failed to read {}: {e}", config.auth_jwt_public_key_path))?;
    let decoding_key = DecodingKey::from_rsa_pem(&public_key_pem)?;
    let private_key_pem = std::fs::read_to_string(&config.auth_jwt_private_key_path)
        .map_err(|e| anyhow::anyhow!("failed to read {}: {e}", config.auth_jwt_private_key_path))?;

    let state = AppState::new(
        pool,
        metadata_base,
        metadata,
        Arc::new(permissions),
        decoding_key,
        private_key_pem,
        router,
    );

    // No lowcode/control_http extra routes — this PoC doesn't need the low-code admin API or
    // platform-tenant provisioning surface.
    let router = build_router(state, &config.cors_origins, axum::Router::new());

    let addr = format!("{}:{}", config.host, config.port);
    let listener = tokio::net::TcpListener::bind(&addr).await?;
    tracing::info!(%addr, "listening");

    axum::serve(
        listener,
        router.into_make_service_with_connect_info::<std::net::SocketAddr>(),
    )
    .with_graceful_shutdown(shutdown_signal())
    .await?;

    Ok(())
}

async fn shutdown_signal() {
    tokio::signal::ctrl_c().await.ok();
    tracing::info!("shutdown signal received, exiting");
}
