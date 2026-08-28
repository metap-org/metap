//! `EventBus` (the publish/subscribe trait), `ConsumedEvent` (one delivery, with its
//! ack/nack/retry methods), and `RetryPolicy` — the backend-agnostic shapes `super::rabbit`'s
//! `RabbitEventBus` implements/produces. See `super`'s doc comment for why this is a trait.

use std::time::Duration;

use async_trait::async_trait;
use futures_util::stream::BoxStream;
use lapin::message::Delivery;
use lapin::options::{BasicAckOptions, BasicNackOptions, BasicPublishOptions};
use lapin::types::AMQPValue;
use lapin::BasicProperties;

pub(crate) const EXCHANGE: &str = "metap.events";

/// Custom header `retry_or_give_up` stamps onto a retry-tier publish, carrying the message's
/// true original routing key. Needed because the retry-tier queue's own `x-dead-letter-routing-key`
/// is set to the plain queue name (deliberately — so its default-exchange redelivery reaches
/// only that one queue directly, not every consumer bound to the shared topic exchange the way
/// republishing via `EXCHANGE` with the original key would) — but AMQP overwrites a message's
/// `routing_key` with whatever the dead-letter *republish* used, so without this header a
/// redelivered message's `delivery.routing_key` would read as the bare queue name instead of
/// the topic that actually produced it (`crm.customers.record.created`, say) — breaking any
/// handler that dispatches on `ConsumedEvent::routing_key` (`cron-scheduler::trigger`'s
/// `classify_topic`, `HandlerRegistry`'s pattern matching). `RabbitEventBus::subscribe` reads
/// this header back (falling back to `delivery.routing_key` when absent, i.e. every non-retried
/// message) to reconstruct the true routing key regardless of how many retry hops a message has
/// been through.
pub(crate) const ORIGINAL_ROUTING_KEY_HEADER: &str = "x-original-routing-key";

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
    pub(crate) delivery: Delivery,
    /// Only populated (and only needed) when the caller wants `retry_or_give_up` — a clone of
    /// the subscribing channel (cheap: `lapin::Channel` is an `Arc`-backed handle) and the
    /// origin queue's name, so a retry can be published directly to `<queue>.retry.<n>` without
    /// the caller needing to hold onto the `EventBus`/bus instance itself (`run_resilient_consumer`'s
    /// handler closures only ever see the `ConsumedEvent`, never the bus).
    pub(crate) retry_channel: lapin::Channel,
    pub(crate) queue: String,
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
        // more than one hop. Also (re-)stamp the original-routing-key header from `self.routing_key`
        // (already correctly reconstructed by `subscribe`, whatever hop this delivery is on) —
        // see `ORIGINAL_ROUTING_KEY_HEADER`'s doc comment for why this is needed at all.
        let mut headers = self.delivery.properties.headers().as_ref().cloned().unwrap_or_default();
        headers.insert(
            ORIGINAL_ROUTING_KEY_HEADER.into(),
            AMQPValue::LongString(self.routing_key.clone().into()),
        );
        let props = BasicProperties::default().with_delivery_mode(2).with_headers(headers);
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
