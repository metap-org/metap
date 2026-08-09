use metap_infra::{load_config, EventBus, RabbitEventBus};

#[tokio::main]
async fn main() -> anyhow::Result<()> {
    metap_infra::init_tracing();
    let config = load_config()?;

    tracing::info!("connecting to rabbitmq...");
    let bus = RabbitEventBus::connect(&config.rabbitmq_url).await?;

    tracing::info!(
        queue = notification_worker::QUEUE,
        routing_key = notification_worker::ROUTING_KEY,
        "ready, listening"
    );

    notification_worker::run(&bus, shutdown_signal()).await?;

    bus.close().await.ok();
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
