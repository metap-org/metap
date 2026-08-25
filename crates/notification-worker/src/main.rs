use metap_infra::{load_config, RabbitEventBus};

#[tokio::main]
async fn main() -> anyhow::Result<()> {
    metap_infra::init_tracing();
    let config = load_config()?;

    tracing::info!(
        queue = notification_worker::QUEUE,
        routing_key = notification_worker::ROUTING_KEY,
        "ready, listening"
    );

    let url = config.rabbitmq_url.clone();
    notification_worker::run(
        move || {
            let url = url.clone();
            async move { RabbitEventBus::connect(&url).await }
        },
        shutdown_signal(),
    )
    .await?;

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
