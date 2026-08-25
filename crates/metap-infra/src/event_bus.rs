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

use std::time::Duration;

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

/// A per-message retry-with-backoff schedule for `ConsumedEvent::retry_or_give_up` — distinct
/// from `run_resilient_consumer`'s `backoff_delay` (that one backs off *reconnecting the whole
/// consumer* after a connection loss; this one backs off *redelivering one specific message*
/// after its handler failed transiently). `delays[i]` is how long the message waits before its
/// `i+1`-th redelivery; once every delay is exhausted the message is given up on (final DLQ).
#[derive(Debug, Clone)]
pub struct RetryPolicy {
    pub delays: Vec<Duration>,
}

/// A message received from `EventBus::subscribe`, already parsed as JSON. Must be acked (or
/// nacked) exactly once — the underlying queue is durable, so an unacked delivery is
/// redelivered on reconnect rather than lost.
pub struct ConsumedEvent {
    pub routing_key: String,
    pub payload: serde_json::Value,
    delivery: Delivery,
    /// Only populated (and only needed) when the caller wants `retry_or_give_up` — a clone of
    /// the subscribing channel (cheap: `lapin::Channel` is an `Arc`-backed handle) and the
    /// origin queue's name, so a retry can be published directly to `<queue>.retry.<n>` without
    /// the caller needing to hold onto the `EventBus`/bus instance itself (`run_resilient_consumer`'s
    /// handler closures only ever see the `ConsumedEvent`, never the bus).
    retry_channel: lapin::Channel,
    queue: String,
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

    /// How many times this exact message has already gone through a retry tier — read straight
    /// from the AMQP `x-death` header RabbitMQ itself maintains (one entry per distinct queue a
    /// message has been dead-lettered from; each retry tier is a distinct queue name, so the
    /// array's length is exactly the retry count) rather than a custom header, so this stays
    /// correct even across a process restart mid-retry.
    pub fn retry_count(&self) -> u32 {
        let Some(headers) = self.delivery.properties.headers() else {
            return 0;
        };
        let Some(AMQPValue::FieldArray(deaths)) = headers.inner().get("x-death") else {
            return 0;
        };
        deaths.as_slice().len() as u32
    }

    /// Retries this message with backoff per `policy`, or gives up (final DLQ) once every delay
    /// in it is exhausted — the bounded, backed-off alternative to calling `nack(requeue: true)`
    /// (immediate, unbounded redelivery) or `nack(requeue: false)` (give up on the first
    /// failure) directly. Always resolves the original delivery one way or the other (ack after
    /// re-publishing to a retry tier, or `nack(false)` once exhausted) — never leaves it
    /// unacked. Requires `subscribe`'s `retry_policy` to have declared the same tiers `policy`
    /// describes (`RabbitEventBus::subscribe` does this when given `Some(policy)`); retrying
    /// against undeclared tiers would fail the publish.
    pub async fn retry_or_give_up(&self, policy: &RetryPolicy) -> anyhow::Result<()> {
        let attempt = self.retry_count() as usize;
        if attempt >= policy.delays.len() {
            tracing::warn!(
                queue = self.queue,
                attempt,
                "retry attempts exhausted, routed to dead-letter queue"
            );
            return self.nack(false).await;
        }

        let retry_queue = format!("{}.retry.{}", self.queue, attempt + 1);
        let payload_bytes = serde_json::to_vec(&self.payload)?;
        // Forward whatever headers this delivery already carries (including any `x-death`
        // entries from earlier retry hops) — RabbitMQ's own TTL-triggered dead-letter on the
        // tier queue appends the *next* `x-death` entry on top of these when it routes the
        // message back to the main queue, which is what keeps `retry_count()` accurate across
        // more than one hop.
        let mut props = BasicProperties::default().with_delivery_mode(2);
        if let Some(headers) = self.delivery.properties.headers() {
            props = props.with_headers(headers.clone());
        }
        self.retry_channel
            .basic_publish("", &retry_queue, BasicPublishOptions::default(), &payload_bytes, props)
            .await?;
        self.ack().await
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
    /// caller expects to clear quickly, not as a general retry mechanism.
    ///
    /// `retry_policy`, when `Some`, additionally declares one queue per `RetryPolicy::delays`
    /// entry (`<queue>.retry.1`, `<queue>.retry.2`, ...) so `ConsumedEvent::retry_or_give_up`
    /// has somewhere to publish a backed-off redelivery — `None` (every caller before this
    /// parameter existed) skips that, unchanged behavior.
    async fn subscribe(
        &self,
        queue: &str,
        routing_key: &str,
        retry_policy: Option<&RetryPolicy>,
    ) -> anyhow::Result<BoxStream<'static, ConsumedEvent>>;

    async fn close(&self) -> anyhow::Result<()>;
}

/// Backoff schedule for `run_resilient_consumer`'s reconnect attempts — exponential, capped at
/// 30s (1s, 2s, 4s, 8s, 16s, 30s, 30s, ...): long enough that a broker restart/failover isn't
/// hammered with reconnect attempts, short enough that a transient blip recovers quickly.
fn backoff_delay(attempt: u32) -> std::time::Duration {
    std::time::Duration::from_secs(2u64.saturating_pow(attempt.min(4)).min(30))
}

/// `false` if `shutdown` resolved first — the caller should stop retrying and exit rather than
/// sleeping through a requested shutdown.
async fn sleep_or_shutdown(
    delay: std::time::Duration,
    shutdown: &mut (impl std::future::Future<Output = ()> + Unpin),
) -> bool {
    tokio::select! {
        biased;
        _ = &mut *shutdown => false,
        _ = tokio::time::sleep(delay) => true,
    }
}

/// Runs `handle` for every event on `(queue, routing_key)` until `shutdown` resolves,
/// reconnecting with backoff (`backoff_delay`) on any connection loss instead of requiring a
/// full process restart. Every consumer of `EventBus::subscribe` in this codebase
/// (`notification-worker`, `cron-scheduler`'s executor and trigger-listener) used to have the
/// identical "subscribe once, bail on disconnect, let a process manager restart" shape — this
/// is that shape's replacement, generalized so the reconnect/backoff logic lives in exactly one
/// place instead of being copy-pasted per consumer.
///
/// `connect` is called again for every (re)connection attempt — a real reconnect needs a fresh
/// `Connection`, not just a retried `subscribe` on a bus whose connection is already dead — so
/// this owns the bus's lifecycle (connects it, closes it on every disconnect and on shutdown)
/// rather than taking an already-connected `&impl EventBus`. `handle` is responsible for
/// ack/nack-ing each event itself (the right choice differs per consumer — some nack on parse
/// failure, `notification-worker` always acks); this function never acks/nacks on its own.
pub async fn run_resilient_consumer<B, F, Fut, H, HFut>(
    queue: &str,
    routing_key: &str,
    retry_policy: Option<&RetryPolicy>,
    connect: F,
    mut handle: H,
    shutdown: impl std::future::Future<Output = ()>,
) -> anyhow::Result<()>
where
    B: EventBus,
    F: Fn() -> Fut,
    Fut: std::future::Future<Output = anyhow::Result<B>>,
    H: FnMut(ConsumedEvent) -> HFut,
    HFut: std::future::Future<Output = ()>,
{
    let mut shutdown = std::pin::pin!(shutdown);
    let mut attempt: u32 = 0;

    'reconnect: loop {
        let bus = match connect().await {
            Ok(bus) => bus,
            Err(err) => {
                tracing::warn!(error = %err, attempt, queue, "failed to connect to event bus, retrying");
                if !sleep_or_shutdown(backoff_delay(attempt), &mut shutdown).await {
                    return Ok(());
                }
                attempt += 1;
                continue 'reconnect;
            }
        };

        let mut events = match bus.subscribe(queue, routing_key, retry_policy).await {
            Ok(events) => events,
            Err(err) => {
                tracing::warn!(error = %err, attempt, queue, "failed to subscribe, reconnecting");
                bus.close().await.ok();
                if !sleep_or_shutdown(backoff_delay(attempt), &mut shutdown).await {
                    return Ok(());
                }
                attempt += 1;
                continue 'reconnect;
            }
        };
        if attempt > 0 {
            tracing::info!(attempt, queue, "reconnected to event bus");
        }
        attempt = 0;

        loop {
            tokio::select! {
                biased;
                _ = &mut shutdown => {
                    tracing::info!(queue, "shutdown signal received, exiting");
                    bus.close().await.ok();
                    return Ok(());
                }
                event = events.next() => {
                    let Some(event) = event else {
                        tracing::warn!(queue, "event stream closed unexpectedly (bus disconnected?), reconnecting");
                        bus.close().await.ok();
                        continue 'reconnect;
                    };
                    handle(event).await;
                }
            }
        }
    }
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
                        Ok(payload) => Some(ConsumedEvent {
                            routing_key: delivery.routing_key.to_string(),
                            payload,
                            delivery,
                            retry_channel,
                            queue: queue_name.to_string(),
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
