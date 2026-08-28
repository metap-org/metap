//! `run_resilient_consumer` — the reconnect-with-backoff wrapper every `EventBus::subscribe`
//! consumer in this codebase runs through, instead of each hand-rolling "subscribe once, bail
//! on disconnect, let a process manager restart".

use futures_util::StreamExt;

use super::types::{ConsumedEvent, EventBus, RetryPolicy};

/// Backoff schedule for reconnecting to the event bus after a connection loss — exponential,
/// capped at 30s (1s, 2s, 4s, 8s, 16s, 30s, 30s, ...): long enough that a broker restart/failover
/// isn't hammered with reconnect attempts, short enough that a transient blip recovers quickly.
/// `pub` (not just `run_resilient_consumer`'s private helper) since `outbox-publisher` needs the
/// exact same schedule for its own reconnect loop on the *publish* side, which
/// `run_resilient_consumer` doesn't cover (it's subscribe-only).
pub fn backoff_delay(attempt: u32) -> std::time::Duration {
    std::time::Duration::from_secs(2u64.saturating_pow(attempt.min(4)).min(30))
}

/// `false` if `shutdown` resolved first — the caller should stop retrying and exit rather than
/// sleeping through a requested shutdown. `pub` for the same reason as `backoff_delay`.
pub async fn sleep_or_shutdown(
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
