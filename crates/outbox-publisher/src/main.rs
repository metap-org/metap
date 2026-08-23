use std::env;

use metap_infra::{connect_db, load_config, EventBus, RabbitEventBus};

#[tokio::main]
async fn main() -> anyhow::Result<()> {
    metap_infra::init_tracing();
    let config = load_config()?;

    let poll_ms: u64 = env::var("OUTBOX_POLL_MS")
        .ok()
        .and_then(|v| v.parse().ok())
        .unwrap_or(1000);
    let batch_size: i64 = env::var("OUTBOX_BATCH_SIZE")
        .ok()
        .and_then(|v| v.parse().ok())
        .unwrap_or(100);

    tracing::info!("connecting to postgres...");
    let pool = connect_db(config.outbox_database_url()).await?;

    tracing::info!("connecting to rabbitmq...");
    let bus = RabbitEventBus::connect(&config.rabbitmq_url).await?;

    tracing::info!(poll_ms, batch_size, "ready, polling");

    outbox_publisher::run(&pool, &bus, poll_ms, batch_size, shutdown_signal()).await?;

    bus.close().await.ok();
    pool.close().await;

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
