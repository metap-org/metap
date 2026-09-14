use metap_infra::load_config;

#[tokio::main]
async fn main() -> anyhow::Result<()> {
    metap_infra::init_tracing();
    let config = load_config()?;

    tracing::info!(
        queue = notification_worker::QUEUE,
        routing_key = notification_worker::ROUTING_KEY,
        "ready, listening"
    );

    notification_worker::run(
        metap_infra::rabbitmq_connector(config.rabbitmq_url.clone()),
        metap_runtime::shutdown::signal(),
    )
    .await?;

    Ok(())
}
