use std::sync::Arc;
use std::time::Duration;

use metap_control::{PostgresTenantRegistry, RegistryCache, Router, SecretStore};
use metap_infra::{connect_db, load_config};
use reconciler_orchestrator::{run, OrchestratorConfig};

#[tokio::main]
async fn main() -> anyhow::Result<()> {
    metap_infra::init_tracing();
    let config = load_config()?;

    let poll_ms: u64 = std::env::var("RECONCILER_POLL_MS")
        .ok()
        .and_then(|v| v.parse().ok())
        .unwrap_or(5000);
    let batch_limit: i64 = std::env::var("RECONCILER_BATCH_LIMIT")
        .ok()
        .and_then(|v| v.parse().ok())
        .unwrap_or(10);
    let max_attempts: i32 = std::env::var("RECONCILER_MAX_ATTEMPTS")
        .ok()
        .and_then(|v| v.parse().ok())
        .unwrap_or(5);
    // Default matches `docs/multi-tenant-platform-design.md` §6.3's "trial (schema chung 1 DB)
    // → concurrency THẤP" guidance — every `Schema`-strategy tenant this loop reconciles shares
    // one physical database, so a high default here would let concurrent `CREATE INDEX
    // CONCURRENTLY`/backfill ops from unrelated tenants contend for the same DB at once.
    let concurrency_limit: usize = std::env::var("RECONCILER_CONCURRENCY")
        .ok()
        .and_then(|v| v.parse().ok())
        .unwrap_or(2);
    let worker_id = std::env::var("RECONCILER_WORKER_ID")
        .unwrap_or_else(|_| format!("reconciler-orchestrator-{}", std::process::id()));

    tracing::info!("connecting to postgres...");
    let pool = connect_db(&config.database_url).await?;

    // Same Vault/EnvStore branching `apps/crm-server/src/main.rs` uses to build its own
    // `Router` — duplicated rather than shared because there's no lower-level crate both this
    // binary and `crm-server` could pull it from without introducing a new dependency neither
    // otherwise needs; see `metap-control`'s own doc comment on `SecretStore` for why this
    // branch exists at all (`docs/roadmap.md` Phase 16 Giai đoạn 4).
    let secret_store: Arc<dyn SecretStore> = match &config.vault_addr {
        Some(addr) => match (&config.vault_role_id, &config.vault_secret_id, &config.vault_token) {
            (Some(role_id), Some(secret_id), _) => {
                let mount = config.vault_approle_mount.as_deref().unwrap_or("approle");
                Arc::new(metap_control::VaultStore::new_with_approle(addr, mount, role_id, secret_id).await?)
            }
            (_, _, Some(token)) => Arc::new(metap_control::VaultStore::new(addr, token)?),
            _ => anyhow::bail!("VAULT_ADDR is set but neither VAULT_TOKEN nor VAULT_ROLE_ID+VAULT_SECRET_ID is"),
        },
        None => Arc::new(metap_control::EnvStore),
    };
    let tenant_registry = Arc::new(PostgresTenantRegistry::new(pool.clone()));
    let router = Router::new(pool.clone(), RegistryCache::new(tenant_registry), secret_store);

    let orchestrator_config = OrchestratorConfig {
        worker_id,
        poll_interval: Duration::from_millis(poll_ms),
        batch_limit,
        max_attempts,
        concurrency_limit,
        // Optional shard-by-entity knob — unset by default (one global queue, every entity).
        entity_name_filter: std::env::var("RECONCILER_ENTITY_FILTER").ok().filter(|s| !s.is_empty()),
    };
    tracing::info!(
        worker_id = orchestrator_config.worker_id,
        poll_ms,
        batch_limit,
        max_attempts,
        concurrency_limit,
        "ready, polling reconciler_entity_deployments"
    );

    let result = run(pool.clone(), router, orchestrator_config, shutdown_signal()).await;
    pool.close().await;
    result
}

async fn shutdown_signal() {
    let ctrl_c = async {
        tokio::signal::ctrl_c().await.ok();
    };

    #[cfg(unix)]
    let terminate = async {
        tokio::signal::unix::signal(tokio::signal::unix::SignalKind::terminate())
            .expect("failed to install SIGTERM handler")
            .recv()
            .await;
    };

    #[cfg(not(unix))]
    let terminate = std::future::pending::<()>();

    tokio::select! {
        _ = ctrl_c => {}
        _ = terminate => {}
    }
}
