//! `apps/jira-server` — a second demo/proof app (`apps/crm-server` is the first), built
//! specifically to wire `metap_reconciler::reconcile()` into a real boot sequence for the first
//! time (`docs/roadmap.md`'s jira-server "bước 6"): `jira.projects`/`jira.issues` both use a
//! dedicated table (`table_name != "records"`, see `entities/`), reconciled here before the
//! server starts serving. Everything else mirrors `apps/crm-server/src/main.rs`'s boot sequence
//! as closely as possible, minus what this PoC doesn't need: no DB-authored (low-code) entity
//! merge, no `lowcode_http`/`control_http` router, no static frontend.
//!
//! **`OUTBOX_WORKER_INLINE=true` runs `outbox-publisher`'s drain loop in this same process,
//! against `tenant_pool` (below), not `pool`.** Found live (2026-08-24): this tenant's
//! `outbox_events` rows — every `jira.issues` create/transition — sat unpublished forever with
//! nothing draining them, because they live in the tenant's own dedicated database
//! (`Router::begin`/`pool_for`), and the standalone `outbox-publisher` binary's normal
//! deployment shape (`pnpm worker:outbox:rs`) points at `apps/crm-server`'s own `.env` /
//! `DATABASE_URL` — the platform's database, never this tenant's. A `DedicatedDb` tenant's
//! outbox needs *something* draining its own database specifically; a separate `outbox-publisher`
//! process with its own `.env` pointed at the tenant's DSN would also work (same shape as
//! `crm-server`'s), but the inline flag is simpler for a single-tenant demo app that already
//! resolves `tenant_pool` at boot anyway — same "two deployment shapes, one shared `run()`,
//! can't drift apart" reasoning `crm-server`'s `NOTIFICATION_WORKER_INLINE` already established.
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
    comment_entity::comment_entity, epic_entity::epic_entity, issue_entity::issue_entity,
    issue_link_entity::issue_link_entity, project_entity::project_entity, sprint_entity::sprint_entity,
    watcher_entity::watcher_entity, worklog_entity::worklog_entity,
};

use std::sync::Arc;

use arc_swap::ArcSwap;
use jsonwebtoken::DecodingKey;
use metap::infra::RabbitEventBus;
use metap::prelude::*;
use secrecy::SecretString;
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
    registry.register(epic_entity())?;
    registry.register(issue_entity())?;
    registry.register(comment_entity())?;
    registry.register(issue_link_entity())?;
    registry.register(worklog_entity())?;
    registry.register(watcher_entity())?;
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
    // entity that references it. project -> sprint (references project) -> epic (references
    // project) -> issue (references project + sprint + epic + itself via parentIssue) ->
    // comment (references issue) -> issue_links (references issue twice).
    for entity in [
        project_entity(),
        sprint_entity(),
        epic_entity(),
        issue_entity(),
        comment_entity(),
        issue_link_entity(),
        worklog_entity(),
        watcher_entity(),
    ] {
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

    let mut state = AppState::new(
        pool,
        metadata_base,
        metadata,
        Arc::new(permissions),
        decoding_key,
        private_key_pem,
        router,
    );

    // Off by default — only set if `S3_BUCKET` is configured (same "presence of the one
    // required knob opts a feature in" convention `POLICY_CACHE_REDIS_URL`/
    // `OUTBOX_WORKER_INLINE` already use). Backs `metap-http`'s generic
    // `/api/{entity}/{id}/attachments*` routes (`metap-storage::ObjectStore`'s first real
    // consumer anywhere in this repo, added ahead of a consumer in Phase 22 — this is that
    // consumer landing) — every entity in this app (`jira.issues`, etc) gets file attachments
    // for free, nothing jira-specific to wire beyond this.
    if let Ok(bucket) = std::env::var("S3_BUCKET") {
        let store = metap_storage::S3ObjectStore::new(metap_storage::S3ObjectStoreConfig {
            endpoint_url: std::env::var("S3_ENDPOINT_URL").ok(),
            region: std::env::var("S3_REGION").unwrap_or_else(|_| "us-east-1".to_string()),
            access_key: SecretString::from(std::env::var("S3_ACCESS_KEY").unwrap_or_default()),
            secret_key: SecretString::from(std::env::var("S3_SECRET_KEY").unwrap_or_default()),
            bucket,
            // SeaweedFS (this project's dev S3 backend, `docker-compose.yml`'s `seaweedfs`
            // service) needs path-style addressing, not virtual-hosted-style — see
            // `metap-storage::S3ObjectStoreConfig`'s doc comment.
            force_path_style: true,
        });
        state.object_store = Some(Arc::new(store));
        tracing::info!("object storage configured");
    }

    // No lowcode/control_http extra routes — this PoC doesn't need the low-code admin API or
    // platform-tenant provisioning surface. Attachment routes are generic now
    // (`metap-http::routes::attachments`, always registered in `build_router` itself) — no
    // jira-specific route module needed here anymore.
    let router = build_router(state, &config.cors_origins, axum::Router::new());

    // Off by default — see this file's top doc comment for why this tenant's outbox needs
    // *something* draining it, and why inline (against `tenant_pool`, not `pool`) is the
    // simplest option for this single-tenant demo app. `OUTBOX_POLL_MS`/`OUTBOX_BATCH_SIZE`
    // read the same env vars the standalone `outbox-publisher` binary does, for the same reason
    // (no shared config struct field — each caller reads its own knobs at its own composition
    // root).
    let outbox_worker_handle = if env_flag_enabled("OUTBOX_WORKER_INLINE") {
        let outbox_pool = tenant_pool.clone();
        let poll_ms: u64 = std::env::var("OUTBOX_POLL_MS")
            .ok()
            .and_then(|v| v.parse().ok())
            .unwrap_or(1000);
        let batch_size: i64 = std::env::var("OUTBOX_BATCH_SIZE")
            .ok()
            .and_then(|v| v.parse().ok())
            .unwrap_or(100);
        let rabbitmq_url = config.rabbitmq_url.clone();
        let connect = move || {
            let url = rabbitmq_url.clone();
            async move { RabbitEventBus::connect(&url).await }
        };
        Some(tokio::spawn(async move {
            if let Err(err) = outbox_publisher::run(&outbox_pool, connect, poll_ms, batch_size, shutdown_signal()).await
            {
                tracing::error!(error = %err, "outbox worker exited with error");
            }
        }))
    } else {
        None
    };

    let addr = format!("{}:{}", config.host, config.port);
    let listener = tokio::net::TcpListener::bind(&addr).await?;
    tracing::info!(%addr, "listening");

    axum::serve(
        listener,
        router.into_make_service_with_connect_info::<std::net::SocketAddr>(),
    )
    .with_graceful_shutdown(shutdown_signal())
    .await?;

    // Block on the inline outbox worker's own shutdown branch (and its `bus.close()`) instead
    // of letting the process exit as soon as the HTTP server drains — otherwise the spawned
    // task above can be cut off mid-publish, same reasoning `crm-server`'s inline notification
    // worker already applies.
    if let Some(handle) = outbox_worker_handle {
        handle.await.ok();
    }

    Ok(())
}

async fn shutdown_signal() {
    tokio::signal::ctrl_c().await.ok();
    tracing::info!("shutdown signal received, exiting");
}

fn env_flag_enabled(name: &str) -> bool {
    std::env::var(name).is_ok_and(|v| v == "true" || v == "1")
}
