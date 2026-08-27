use std::sync::Arc;
use std::time::Duration;

use metap_control::{PostgresTenantRegistry, RegistryCache, Router};
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

    // `metap_control::build_secret_store` picks the right `SecretStore` from env — same helper
    // `apps/crm-server`/`apps/jira-server` use to build their own `Router`, see that function's
    // doc comment for the precedence order across `EnvStore`/`VaultStore`/`AwsSecretsManagerStore`/
    // `GcpSecretManagerStore`.
    let secret_store = metap_control::build_secret_store(&config).await?;
    let tenant_registry = Arc::new(PostgresTenantRegistry::new(pool.clone()));
    let router = Router::new(pool.clone(), RegistryCache::new(tenant_registry), secret_store);
    // A second, separate handle (not the `Arc` above, already moved into `RegistryCache`) —
    // `run`'s `DedicatedDb` fan-out (`run_tick`) needs `PostgresTenantRegistry::list`, which
    // isn't on the `TenantRegistry` trait `RegistryCache` wraps. Cheap: just another `PgPool`
    // clone, same handle shape `Router::new` above already takes one of.
    let fanout_tenant_registry = PostgresTenantRegistry::new(pool.clone());

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

    let result = run(
        pool.clone(),
        router,
        fanout_tenant_registry,
        orchestrator_config,
        shutdown_signal(),
    )
    .await;
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
