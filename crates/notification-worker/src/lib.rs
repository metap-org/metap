//! Entity-agnostic notification consumer: logs every workflow transition event. This is the
//! first real consumer of `EventBus::subscribe` (`crates/metap-infra`) — see
//! `docs/roadmap.md` Phase 5's note on the `<entity>.workflow.transitioned` topic having been
//! a publish-only stub until now. Deliberately minimal (stdout logging, not email/SMS/webhook)
//! since no actual notification channel has been asked for yet; the point of this crate is the
//! consume-loop plumbing (durable queue, ack/nack, graceful shutdown), which any real
//! notification channel would reuse.

use futures_util::StreamExt;
use metap_infra::{ConsumedEvent, EventBus};

pub const QUEUE: &str = "notification.workflow-transitioned";
/// `#` matches any number of dot-separated words, so this binds every entity's transition
/// topic regardless of how domain-namespaced its name is (e.g. `crm.customers.workflow.transitioned`).
pub const ROUTING_KEY: &str = "#.workflow.transitioned";

/// Runs the consume loop until `shutdown` resolves. Reusable both by this crate's own
/// standalone binary (`src/main.rs`, a separate deployable process) and by a host process that
/// wants to run the worker in the same process instead (see `apps/crm-server`'s
/// `NOTIFICATION_WORKER_INLINE` flag) — same function either way, so the two deployment shapes
/// can't drift apart.
pub async fn run(
    bus: &impl EventBus,
    shutdown: impl std::future::Future<Output = ()>,
) -> anyhow::Result<()> {
    let mut events = bus.subscribe(QUEUE, ROUTING_KEY).await?;
    let mut shutdown = std::pin::pin!(shutdown);

    loop {
        tokio::select! {
            biased;
            _ = &mut shutdown => {
                tracing::info!("shutdown signal received, exiting");
                return Ok(());
            }
            event = events.next() => {
                // Distinct from the shutdown branch above: the stream only ends this way on
                // an unrequested disconnect (e.g. RabbitMQ connection dropped), never on a
                // normal ctrl-c/SIGTERM (that's caught by the `shutdown` branch instead,
                // which returns before this arm can run again). Propagating an error here —
                // matching `outbox-publisher`'s contract — lets a process manager tell "worker
                // crashed, restart it" apart from "worker exited on request."
                let Some(event) = event else {
                    anyhow::bail!("event stream closed unexpectedly (bus disconnected?)");
                };
                notify(&event);
                event.ack().await.ok();
            }
        }
    }
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
