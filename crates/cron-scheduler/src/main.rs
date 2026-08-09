use std::env;
use std::time::Duration;

use cron_scheduler::{run_executor, run_ticker, ExecutorConfig, TickerConfig};
use metap_infra::{connect_db, load_config, EventBus, RabbitEventBus};

#[tokio::main]
async fn main() -> anyhow::Result<()> {
    metap_infra::init_tracing();
    let config = load_config()?;

    let tick_ms: u64 =
        env::var("CRON_TICK_MS").ok().and_then(|v| v.parse().ok()).unwrap_or(5000);
    let batch_size: i64 =
        env::var("CRON_BATCH_SIZE").ok().and_then(|v| v.parse().ok()).unwrap_or(50);
    let target_base_url = env::var("CRON_TARGET_BASE_URL")
        .ok()
        .filter(|s| !s.is_empty())
        .unwrap_or_else(|| format!("http://localhost:{}", config.port));
    let service_jwt = env::var("CRON_SERVICE_JWT").unwrap_or_default();
    if service_jwt.is_empty() {
        tracing::warn!(
            "CRON_SERVICE_JWT is unset — workflow_transition/bulk_query_action jobs will fail; \
             webhook jobs are unaffected. Mint one with `pnpm mint-token` and grant it whatever \
             role the jobs it runs need."
        );
    }

    tracing::info!("connecting to postgres...");
    let pool = connect_db(config.outbox_database_url()).await?;

    tracing::info!("connecting to rabbitmq...");
    let bus = RabbitEventBus::connect(&config.rabbitmq_url).await?;

    let http = reqwest::Client::builder().timeout(Duration::from_secs(30)).build()?;
    let executor_config = ExecutorConfig { target_base_url, service_jwt };
    let ticker_config = TickerConfig { interval: Duration::from_millis(tick_ms), batch_size };

    tracing::info!(
        tick_ms,
        batch_size,
        target_base_url = executor_config.target_base_url,
        "ready, ticking and listening"
    );

    let ticker = run_ticker(&pool, &http, &executor_config, ticker_config, shutdown_signal());
    let executor = run_executor(&bus, &pool, &http, &executor_config, shutdown_signal());
    let result = tokio::try_join!(ticker, executor);

    bus.close().await.ok();
    pool.close().await;

    result?;
    Ok(())
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
