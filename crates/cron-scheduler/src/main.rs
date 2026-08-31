use std::env;
use std::time::Duration;

use cron_scheduler::{run_executor, run_ticker, run_trigger_listener, ExecutorConfig, SmtpConfig, TickerConfig};
use metap_infra::{connect_db, load_config, RabbitEventBus};

#[tokio::main]
async fn main() -> anyhow::Result<()> {
    metap_infra::init_tracing();
    let config = load_config()?;

    let tick_ms: u64 = metap_runtime::env::env_or("CRON_TICK_MS", 5000);
    let batch_size: i64 = metap_runtime::env::env_or("CRON_BATCH_SIZE", 50);
    let target_base_url = metap_runtime::env::optional("CRON_TARGET_BASE_URL")
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

    let rabbitmq_url = config.rabbitmq_url.clone();
    let http = metap_runtime::http_client::default_client();
    let executor_config = ExecutorConfig {
        target_base_url,
        service_jwt,
        smtp: SmtpConfig {
            host: config.smtp_host.clone(),
            port: config.smtp_port,
            user: config.smtp_user.clone(),
            password: config.smtp_password.clone(),
            from: config.smtp_from.clone(),
        },
    };
    let ticker_config = TickerConfig {
        interval: Duration::from_millis(tick_ms),
        batch_size,
    };

    tracing::info!(
        tick_ms,
        batch_size,
        target_base_url = executor_config.target_base_url,
        "ready, ticking and listening"
    );

    let executor_connect = {
        let url = rabbitmq_url.clone();
        move || {
            let url = url.clone();
            async move { RabbitEventBus::connect(&url).await }
        }
    };
    let trigger_connect = {
        let url = rabbitmq_url.clone();
        move || {
            let url = url.clone();
            async move { RabbitEventBus::connect(&url).await }
        }
    };

    let ticker = run_ticker(
        &pool,
        &http,
        &executor_config,
        ticker_config,
        metap_runtime::shutdown::signal(),
    );
    let executor = run_executor(
        executor_connect,
        &pool,
        &http,
        &executor_config,
        metap_runtime::shutdown::signal(),
    );
    let trigger = run_trigger_listener(
        trigger_connect,
        &pool,
        &http,
        &executor_config,
        metap_runtime::shutdown::signal(),
    );
    let result = tokio::try_join!(ticker, executor, trigger);

    pool.close().await;

    result?;
    Ok(())
}
