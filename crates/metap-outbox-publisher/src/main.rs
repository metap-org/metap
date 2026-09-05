use metap_infra::{connect_db, load_config, RabbitEventBus};

#[tokio::main]
async fn main() -> anyhow::Result<()> {
    metap_infra::init_tracing();
    let config = load_config()?;

    let poll_ms: u64 = metap_runtime::env::env_or("OUTBOX_POLL_MS", 1000);
    let batch_size: i64 = metap_runtime::env::env_or("OUTBOX_BATCH_SIZE", 100);

    tracing::info!("connecting to postgres...");
    let pool = connect_db(config.outbox_database_url()).await?;

    tracing::info!(poll_ms, batch_size, "ready, polling");

    let rabbitmq_url = config.rabbitmq_url.clone();
    let connect = move || {
        let url = rabbitmq_url.clone();
        async move { RabbitEventBus::connect(&url).await }
    };
    outbox_publisher::run(&pool, connect, poll_ms, batch_size, metap_runtime::shutdown::signal()).await?;

    pool.close().await;

    Ok(())
}
