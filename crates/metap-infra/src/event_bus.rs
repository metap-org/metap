//! `EventBus` — the interface `docs/architecture-review-2026-08-07.md` Part 2 recommended
//! extracting in front of `RabbitPublisher`, following the `PolicyStore` precedent (the one
//! existing seam in the TS codebase). `RabbitEventBus` is its only implementation today;
//! the point of the trait is that a second one (Kafka/NATS, or an in-memory bus for tests)
//! is a new `impl EventBus`, not a rewrite of every call site — see
//! `docs/modular-spi-architecture.md` for the target this generalizes toward.

use async_trait::async_trait;
use lapin::options::{BasicPublishOptions, ExchangeDeclareOptions};
use lapin::{BasicProperties, Connection, ConnectionProperties, ExchangeKind};

const EXCHANGE: &str = "metap.events";

#[async_trait]
pub trait EventBus: Send + Sync {
    async fn publish(&self, topic: &str, payload: &serde_json::Value) -> anyhow::Result<()>;
    async fn close(&self) -> anyhow::Result<()>;
}

/// Mirrors `packages/core/src/infra/messaging/rabbitmq.ts`'s `createRabbitPublisher`:
/// same exchange name/kind, same durable+persistent delivery, same fire-and-forget publish
/// (channel is never put into confirm mode — see `docs/rust-core-viability.md`'s spike
/// results for why awaiting a confirm here would be a pure, measured regression).
pub struct RabbitEventBus {
    connection: Connection,
    channel: lapin::Channel,
}

impl RabbitEventBus {
    pub async fn connect(url: &str) -> anyhow::Result<Self> {
        let connection = Connection::connect(url, ConnectionProperties::default()).await?;
        let channel = connection.create_channel().await?;
        channel
            .exchange_declare(
                EXCHANGE,
                ExchangeKind::Topic,
                ExchangeDeclareOptions {
                    durable: true,
                    ..Default::default()
                },
                Default::default(),
            )
            .await?;
        Ok(Self { connection, channel })
    }
}

#[async_trait]
impl EventBus for RabbitEventBus {
    async fn publish(&self, topic: &str, payload: &serde_json::Value) -> anyhow::Result<()> {
        let payload_bytes = serde_json::to_vec(payload)?;
        self.channel
            .basic_publish(
                EXCHANGE,
                topic,
                BasicPublishOptions::default(),
                &payload_bytes,
                BasicProperties::default()
                    .with_content_type("application/json".into())
                    .with_delivery_mode(2), // persistent
            )
            .await?;
        Ok(())
    }

    async fn close(&self) -> anyhow::Result<()> {
        self.channel.close(200, "shutdown").await.ok();
        self.connection.close(200, "shutdown").await.ok();
        Ok(())
    }
}
