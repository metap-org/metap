//! `EventBus` — a trait in front of `RabbitPublisher` rather than a concrete type (see
//! `docs/architectures/09-adr.md`). `RabbitEventBus` is its only implementation today;
//! the point of the trait is that a second one (Kafka/NATS, or an in-memory bus for tests)
//! is a new `impl EventBus`, not a rewrite of every call site — see
//! `docs/modular-spi-architecture.md` for the target this generalizes toward.
//!
//! `subscribe` is the read-side counterpart, added once a real consumer (the notification
//! worker, see `crates/notification-worker`) needed one — see `docs/roadmap.md` Phase 5's
//! note on the stub `<entity>.workflow.transitioned` topic. `ConsumedEvent` hides the
//! `lapin`-specific delivery/ack machinery behind the same backend-agnostic shape `publish`
//! already uses, so a future non-Rabbit `EventBus` impl doesn't leak here either.

use async_trait::async_trait;
use futures_util::stream::BoxStream;
use futures_util::StreamExt;
use lapin::message::Delivery;
use lapin::options::{
    BasicAckOptions, BasicConsumeOptions, BasicNackOptions, BasicPublishOptions, BasicQosOptions,
    ExchangeDeclareOptions, QueueBindOptions, QueueDeclareOptions,
};
use lapin::types::{AMQPValue, FieldTable};
use lapin::{BasicProperties, Connection, ConnectionProperties, ExchangeKind};

const EXCHANGE: &str = "metap.events";

/// A message received from `EventBus::subscribe`, already parsed as JSON. Must be acked (or
/// nacked) exactly once — the underlying queue is durable, so an unacked delivery is
/// redelivered on reconnect rather than lost.
pub struct ConsumedEvent {
    pub routing_key: String,
    pub payload: serde_json::Value,
    delivery: Delivery,
}

impl ConsumedEvent {
    pub async fn ack(&self) -> anyhow::Result<()> {
        self.delivery.ack(BasicAckOptions::default()).await?;
        Ok(())
    }

    /// `requeue: true` for a transient failure worth retrying, `false` for a message that
    /// will never succeed (bad payload) so it isn't redelivered forever.
    pub async fn nack(&self, requeue: bool) -> anyhow::Result<()> {
        self.delivery
            .nack(BasicNackOptions {
                requeue,
                ..Default::default()
            })
            .await?;
        Ok(())
    }
}

#[async_trait]
pub trait EventBus: Send + Sync {
    async fn publish(&self, topic: &str, payload: &serde_json::Value) -> anyhow::Result<()>;

    /// Declares a durable queue named `queue`, binds it to the shared topic exchange with
    /// `routing_key` (AMQP topic wildcards `*`/`#` allowed, e.g. `#.workflow.transitioned`),
    /// and returns a stream of deliveries on it. Multiple processes subscribing with the same
    /// `queue` name compete for messages (standard AMQP work-queue semantics) — use distinct
    /// queue names for independent consumers of the same routing key.
    ///
    /// Delivery semantics: at-least-once (manual ack; an unacked message is redelivered).
    /// `queue` is declared with a dead-letter target of `<queue>.dlq` (also declared here), so
    /// a message nacked with `requeue: false` — including one this method drops itself because
    /// it isn't valid JSON — lands there instead of vanishing. `nack(requeue: true)` redelivers
    /// immediately with no backoff and no retry cap; it's meant for a transient failure a
    /// caller expects to clear quickly, not as a general retry mechanism — a caller that needs
    /// bounded retries-with-backoff should track its own attempt count (e.g. in the DLQ message
    /// itself, or a side table) rather than relying on this call alone.
    async fn subscribe(
        &self,
        queue: &str,
        routing_key: &str,
    ) -> anyhow::Result<BoxStream<'static, ConsumedEvent>>;

    async fn close(&self) -> anyhow::Result<()>;
}

/// Mirrors `packages/core/src/infra/messaging/rabbitmq.ts`'s `createRabbitPublisher`:
/// same exchange name/kind, same durable+persistent delivery, same fire-and-forget publish
/// (channel is never put into confirm mode — a measured throughput regression, not a
/// correctness requirement here).
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
        tracing::info!(exchange = EXCHANGE, "connected to rabbitmq");
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
        tracing::debug!(%topic, "published event");
        Ok(())
    }

    async fn subscribe(
        &self,
        queue: &str,
        routing_key: &str,
    ) -> anyhow::Result<BoxStream<'static, ConsumedEvent>> {
        // A dedicated channel per subscription — consuming holds a channel open for the
        // stream's whole lifetime, and sharing `self.channel` with `publish` would mean a
        // slow/blocked consumer could stall publishes on the same connection.
        let channel = self.connection.create_channel().await?;

        // Dead-letter target for anything nacked with `requeue: false` (a poison message, or
        // a caller giving up on a bad payload) — routed via the default exchange (`""`),
        // where the routing key is matched directly against a queue name, so declaring this
        // queue is the only setup a dead-letter route needs.
        let dlq_name = format!("{queue}.dlq");
        channel
            .queue_declare(
                &dlq_name,
                QueueDeclareOptions {
                    durable: true,
                    ..Default::default()
                },
                FieldTable::default(),
            )
            .await?;

        let mut queue_args = FieldTable::default();
        queue_args.insert("x-dead-letter-exchange".into(), AMQPValue::LongString("".into()));
        queue_args.insert(
            "x-dead-letter-routing-key".into(),
            AMQPValue::LongString(dlq_name.into()),
        );
        channel
            .queue_declare(
                queue,
                QueueDeclareOptions {
                    durable: true,
                    ..Default::default()
                },
                queue_args,
            )
            .await?;
        channel
            .queue_bind(
                queue,
                EXCHANGE,
                routing_key,
                QueueBindOptions::default(),
                FieldTable::default(),
            )
            .await?;

        // Bounds how many unacked deliveries the broker will push ahead of the consumer —
        // without this the broker has no backpressure signal and will flood an unbounded
        // number of in-flight (unacked) messages to the client.
        const PREFETCH_COUNT: u16 = 20;
        channel.basic_qos(PREFETCH_COUNT, BasicQosOptions::default()).await?;

        let consumer = channel
            .basic_consume(
                queue,
                queue, // consumer tag: unique enough per process, not meaningful beyond logs
                BasicConsumeOptions::default(),
                FieldTable::default(),
            )
            .await?;
        tracing::info!(%queue, %routing_key, "subscribed");

        // Owned + `Arc`'d so the filter_map closure below can be `'static` without borrowing
        // from this function's stack frame.
        let queue_name: std::sync::Arc<str> = std::sync::Arc::from(queue);
        let events = consumer.filter_map(move |delivery| {
            let queue_name = queue_name.clone();
            async move {
                match delivery {
                    Ok(delivery) => match serde_json::from_slice(&delivery.data) {
                        Ok(payload) => Some(ConsumedEvent {
                            routing_key: delivery.routing_key.to_string(),
                            payload,
                            delivery,
                        }),
                        Err(err) => {
                            tracing::warn!(
                                queue = %queue_name,
                                error = %err,
                                "dropping malformed message, routed to dead-letter queue"
                            );
                            delivery.nack(BasicNackOptions::default()).await.ok();
                            None
                        }
                    },
                    Err(err) => {
                        tracing::error!(queue = %queue_name, error = %err, "consumer error");
                        None
                    }
                }
            }
        });
        Ok(events.boxed())
    }

    async fn close(&self) -> anyhow::Result<()> {
        self.channel.close(200, "shutdown").await.ok();
        self.connection.close(200, "shutdown").await.ok();
        Ok(())
    }
}
