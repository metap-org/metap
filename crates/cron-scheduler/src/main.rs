use std::time::Duration;

use cron_scheduler::{run_executor, run_ticker, run_trigger_listener, ExecutorConfig, SmtpConfig, TickerConfig};
use metap_infra::{connect_db, load_config, RabbitEventBus};
use metap_runtime::service_token::ServiceTokenSource;

#[tokio::main]
async fn main() -> anyhow::Result<()> {
    metap_infra::init_tracing();
    let config = load_config()?;

    let tick_ms: u64 = metap_runtime::env::env_or("CRON_TICK_MS", 5000);
    let batch_size: i64 = metap_runtime::env::env_or("CRON_BATCH_SIZE", 50);
    let target_base_url = metap_runtime::env::optional("CRON_TARGET_BASE_URL")
        .unwrap_or_else(|| format!("http://localhost:{}", config.port));
    let http = metap_runtime::http_client::default_client();

    // Logs into the target's own `POST /auth/login` and keeps the token fresh in the background
    // (`metap_runtime::service_token::ServiceTokenSource`) — replaced a static, hand-minted-once
    // `CRON_SERVICE_JWT` 2026-09-02 (same fix `graphql-gateway` got the same day after that exact
    // pattern's 1h TTL expired mid-deployment and crashed a caller at boot). Unlike
    // `graphql-gateway` — whose only job is calling its configured upstreams, so a login failure
    // there should fail the whole boot — this process runs several *other* job types (webhook,
    // email) that don't need this credential at all, so a missing/failing login degrades to an
    // empty token (every `workflow_transition`/`bulk_query_action` job then fails with 401,
    // logged per-job) rather than refusing to start, preserving the original design intent
    // ("webhook jobs are unaffected").
    let login_url = metap_runtime::env::optional("CRON_LOGIN_URL")
        .unwrap_or_else(|| format!("{}/auth/login", target_base_url.trim_end_matches('/')));
    let service_email = metap_runtime::env::optional("CRON_SERVICE_EMAIL");
    let service_password = metap_runtime::env::optional("CRON_SERVICE_PASSWORD");
    let service_token = match (service_email, service_password) {
        (Some(email), Some(password)) => match ServiceTokenSource::start(http.clone(), login_url, email, password).await {
            Ok(source) => source,
            Err(e) => {
                tracing::warn!(error = %e, "failed to log in with CRON_SERVICE_EMAIL/CRON_SERVICE_PASSWORD — workflow_transition/bulk_query_action jobs will fail until this is fixed; webhook jobs are unaffected");
                ServiceTokenSource::from_static("")
            }
        },
        _ => {
            tracing::warn!(
                "CRON_SERVICE_EMAIL/CRON_SERVICE_PASSWORD are unset — workflow_transition/bulk_query_action jobs will \
                 fail; webhook jobs are unaffected. Provision one with `dev-tools create-user`/`dev-tools seed-admin` \
                 and grant it whatever role the jobs it runs need."
            );
            ServiceTokenSource::from_static("")
        }
    };

    tracing::info!("connecting to postgres...");
    let pool = connect_db(config.outbox_database_url()).await?;

    let rabbitmq_url = config.rabbitmq_url.clone();
    let executor_config = ExecutorConfig {
        target_base_url,
        service_token,
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
