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
        metap_runtime::shutdown::signal(),
    )
    .await?;

    Ok(())
}
