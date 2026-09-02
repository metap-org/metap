//! Entity-agnostic notification consumer: logs every workflow transition event. This is the
//! first real consumer of `EventBus::subscribe` (`crates/metap-infra`) — see
//! `docs/roadmap.md` Phase 5's note on the `<entity>.workflow.transitioned` topic having been
//! a publish-only stub until now. Deliberately minimal (stdout logging, not email/SMS/webhook)
//! since no actual notification channel has been asked for yet; the point of this crate is the
//! consume-loop plumbing (durable queue, ack/nack, graceful shutdown, reconnect), which any
//! real notification channel would reuse.

use std::future::Future;

use metap_infra::{run_resilient_consumer, ConsumedEvent, EventBus};

pub const QUEUE: &str = "notification.workflow-transitioned";
/// `#` matches any number of dot-separated words, so this binds every entity's transition
/// topic regardless of how domain-namespaced its name is (e.g. `crm.customers.workflow.transitioned`).
pub const ROUTING_KEY: &str = "#.workflow.transitioned";

/// Runs the consume loop until `shutdown` resolves — a thin wrapper around
/// `metap_infra::run_resilient_consumer` (reconnects with backoff on any connection loss,
/// instead of requiring a full process restart the way this function used to) supplying the
/// notification-specific handler (`notify`, always acks — nothing here can fail in a way worth
/// nacking).
///
/// Reusable both by this crate's own standalone binary (`src/main.rs`, a separate deployable
/// process) and by a host process that wants to run the worker in the same process instead (see
/// `../metap-demo-crm`'s `NOTIFICATION_WORKER_INLINE` flag) — same function either way, so the two
/// deployment shapes can't drift apart.
pub async fn run<B, F, Fut>(connect: F, shutdown: impl Future<Output = ()>) -> anyhow::Result<()>
where
    B: EventBus,
    F: Fn() -> Fut,
    Fut: Future<Output = anyhow::Result<B>>,
{
    run_resilient_consumer(
        QUEUE,
        ROUTING_KEY,
        None, // no retry policy needed — notify() can't fail, always acks
        connect,
        |event| async move {
            notify(&event);
            event.ack().await.ok();
        },
        shutdown,
    )
    .await
}

fn notify(event: &ConsumedEvent) {
    let entity = event
        .routing_key
        .strip_suffix(".workflow.transitioned")
        .unwrap_or(&event.routing_key);
    let record_id = event.payload.get("recordId").and_then(|v| v.as_str()).unwrap_or("?");
    let action = event.payload.get("action").and_then(|v| v.as_str()).unwrap_or("?");
    let from = event.payload.get("from").and_then(|v| v.as_str()).unwrap_or("?");
    let to = event.payload.get("to").and_then(|v| v.as_str()).unwrap_or("?");
    tracing::info!(entity, record_id, action, from, to, "notification");
}
