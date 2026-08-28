//! `RabbitEventBus` — the only `EventBus` impl today (`super`'s doc comment). Mirrors
//! `packages/core/src/infra/messaging/rabbitmq.ts`'s `createRabbitPublisher`: same exchange
//! name/kind, same durable+persistent delivery, same fire-and-forget publish.

use async_trait::async_trait;
use futures_util::stream::BoxStream;
use futures_util::StreamExt;
use lapin::options::{
    BasicConsumeOptions, BasicNackOptions, BasicPublishOptions, BasicQosOptions, ExchangeDeclareOptions,
    QueueBindOptions, QueueDeclareOptions,
};
use lapin::types::{AMQPValue, FieldTable};
use lapin::{BasicProperties, Connection, ConnectionProperties, ExchangeKind};

use super::types::{ConsumedEvent, EventBus, RetryPolicy, EXCHANGE, ORIGINAL_ROUTING_KEY_HEADER};

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
        retry_policy: Option<&RetryPolicy>,
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

        // Retry tiers (`<queue>.retry.1`, `<queue>.retry.2`, ...) — one per `RetryPolicy::delays`
        // entry, only declared when the caller actually wants retry-with-backoff.  No consumer
        // ever attaches to these: a message published here (`ConsumedEvent::retry_or_give_up`)
        // just sits until its `x-message-ttl` expires, at which point RabbitMQ itself
        // dead-letters it back onto `queue` — the classic TTL+DLX "delay queue" pattern, no
        // delayed-message-exchange plugin required (this project's `rabbitmq:3.13-management-alpine`
        // image doesn't ship one).
        if let Some(policy) = retry_policy {
            for (i, delay) in policy.delays.iter().enumerate() {
                let tier_queue = format!("{queue}.retry.{}", i + 1);
                let mut tier_args = FieldTable::default();
                tier_args.insert(
                    "x-message-ttl".into(),
                    AMQPValue::LongInt(delay.as_millis().min(i32::MAX as u128) as i32),
                );
                tier_args.insert("x-dead-letter-exchange".into(), AMQPValue::LongString("".into()));
                tier_args.insert("x-dead-letter-routing-key".into(), AMQPValue::LongString(queue.into()));
                channel
                    .queue_declare(
                        &tier_queue,
                        QueueDeclareOptions {
                            durable: true,
                            ..Default::default()
                        },
                        tier_args,
                    )
                    .await?;
            }
        }

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
        // from this function's stack frame. `channel.clone()` is cheap (an `Arc`-backed handle,
        // same channel `retry_or_give_up` publishes retries on).
        let queue_name: std::sync::Arc<str> = std::sync::Arc::from(queue);
        let retry_channel = channel.clone();
        let events = consumer.filter_map(move |delivery| {
            let queue_name = queue_name.clone();
            let retry_channel = retry_channel.clone();
            async move {
                match delivery {
                    Ok(delivery) => match serde_json::from_slice(&delivery.data) {
                        Ok(payload) => {
                            // A retried (redelivered-via-DLX) message's `delivery.routing_key`
                            // reads as the retry-tier's dead-letter target (the plain queue
                            // name), not the topic that actually produced it — recover the true
                            // one from `ORIGINAL_ROUTING_KEY_HEADER` when present (see its doc
                            // comment). Absent on every non-retried message, which is the common
                            // case — falls back to `delivery.routing_key` there, unchanged.
                            let routing_key = delivery
                                .properties
                                .headers()
                                .as_ref()
                                .and_then(|headers| headers.inner().get(ORIGINAL_ROUTING_KEY_HEADER))
                                .and_then(|value| match value {
                                    AMQPValue::LongString(s) => Some(s.to_string()),
                                    _ => None,
                                })
                                .unwrap_or_else(|| delivery.routing_key.to_string());
                            Some(ConsumedEvent {
                                routing_key,
                                payload,
                                delivery,
                                retry_channel,
                                queue: queue_name.to_string(),
                            })
                        }
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
