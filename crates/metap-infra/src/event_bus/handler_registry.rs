//! `HandlerRegistry` — lets one process register multiple pattern-matched handlers on a single
//! subscription (AMQP topic wildcards via `topic_matches`), instead of each needing its own
//! queue/consumer. See `super`'s doc comment.

use super::resilient::run_resilient_consumer;
use super::types::{ConsumedEvent, EventBus, RetryPolicy};

/// `true` if `routing_key` matches `pattern` under AMQP topic-exchange rules: `.`-separated
/// words, `*` matches exactly one word, `#` matches zero or more words (only meaningful as a
/// whole segment, same as RabbitMQ's own topic matching — `classify_topic`-style suffix/prefix
/// checks elsewhere in this codebase (`cron-scheduler::trigger`) are a special case of this same
/// rule, hand-rolled per caller; `HandlerRegistry` is what generalizes it into a reusable
/// primitive instead of every consumer reimplementing its own matcher).
pub(crate) fn topic_matches(pattern: &str, routing_key: &str) -> bool {
    let pattern: Vec<&str> = pattern.split('.').collect();
    let key: Vec<&str> = routing_key.split('.').collect();
    matches_segments(&pattern, &key)
}

fn matches_segments(pattern: &[&str], key: &[&str]) -> bool {
    match pattern.split_first() {
        None => key.is_empty(),
        Some((&"#", rest)) => {
            if rest.is_empty() {
                return true;
            }
            (0..=key.len()).any(|i| matches_segments(rest, &key[i..]))
        }
        Some((&"*", rest)) => !key.is_empty() && matches_segments(rest, &key[1..]),
        Some((seg, rest)) => key.first() == Some(seg) && matches_segments(rest, &key[1..]),
    }
}

type BoxHandlerResult = std::pin::Pin<Box<dyn std::future::Future<Output = anyhow::Result<()>> + Send>>;
type BoxHandlerFn = Box<dyn Fn(&ConsumedEvent) -> BoxHandlerResult + Send + Sync>;

/// A generic, entity-agnostic mechanism for a process to react to events with more than one
/// independent handler on a single subscription — the piece `notification-worker`
/// (one fixed, unconfigurable handler) and `cron-scheduler`'s trigger-listener (a data-driven
/// dispatch to `cron_jobs` rows, not arbitrary Rust code) don't cover: a business binary
/// (`crm-server`/`jira-server`) that wants its own in-process code to run for `entity.event`
/// patterns it cares about, without hand-rolling a new consume loop (what every consumer in this
/// codebase did before this existed) or spinning up a whole new ops binary. `metap-infra` stays
/// entity-agnostic — the *registry* is generic, but the handler closures registered into it are
/// written and owned by whichever binary calls `.on(...)`, same split as `MetadataRegistry`
/// (generic registry, entity-aware registrations live in the owning binary's `main.rs`).
///
/// One AMQP subscription (`routing_key = "#"`, catch-all — this registry serves N independently
/// registered patterns on one queue, so RabbitMQ's own binding can't do the filtering the way a
/// single-purpose consumer's fixed `routing_key` does) backs every registered handler; matching
/// against each handler's own pattern happens in-process via `topic_matches`.
#[derive(Default)]
pub struct HandlerRegistry {
    handlers: Vec<(String, BoxHandlerFn)>,
}

impl HandlerRegistry {
    pub fn new() -> Self {
        Self { handlers: Vec::new() }
    }

    /// Registers `handler` to run for every event whose routing key matches `pattern` (AMQP
    /// topic wildcards `*`/`#` allowed, same syntax `EventBus::subscribe`'s `routing_key` already
    /// takes). More than one handler can match the same event — see `run`'s doc comment for how
    /// they're run and how a failure is treated.
    pub fn on<F, Fut>(mut self, pattern: impl Into<String>, handler: F) -> Self
    where
        F: Fn(&ConsumedEvent) -> Fut + Send + Sync + 'static,
        Fut: std::future::Future<Output = anyhow::Result<()>> + Send + 'static,
    {
        let boxed: BoxHandlerFn = Box::new(move |event| Box::pin(handler(event)));
        self.handlers.push((pattern.into(), boxed));
        self
    }

    /// Runs every registered handler against every event on `queue` until `shutdown` resolves —
    /// a thin wrapper around `run_resilient_consumer` (same reconnect-with-backoff every other
    /// consumer in this codebase gets). For each event, every handler whose pattern matches runs
    /// concurrently (`futures_util::future::join_all`); the delivery acks only once **all**
    /// matching handlers return `Ok`. Any handler returning `Err` fails the whole event —
    /// `retry_policy` (`Some`) retries it with backoff (`ConsumedEvent::retry_or_give_up`),
    /// `None` dead-letters it immediately. There's no partial-success bookkeeping (ack the
    /// handlers that succeeded, retry only the ones that didn't) — a handler is expected to be
    /// safely re-runnable in full on redelivery, the same at-least-once posture every other
    /// consumer here already has. An event matching no registered handler is acked (not an
    /// error — expected under the catch-all subscription, same reasoning as
    /// `cron-scheduler::trigger`'s `classify_topic` returning `None`).
    pub async fn run<B, F, Fut>(
        self,
        queue: &str,
        retry_policy: Option<&RetryPolicy>,
        connect: F,
        shutdown: impl std::future::Future<Output = ()>,
    ) -> anyhow::Result<()>
    where
        B: EventBus,
        F: Fn() -> Fut,
        Fut: std::future::Future<Output = anyhow::Result<B>>,
    {
        let handlers = std::sync::Arc::new(self.handlers);
        run_resilient_consumer(
            queue,
            "#",
            retry_policy,
            connect,
            move |event| {
                let handlers = handlers.clone();
                async move {
                    let matches: Vec<&BoxHandlerFn> = handlers
                        .iter()
                        .filter(|(pattern, _)| topic_matches(pattern, &event.routing_key))
                        .map(|(_, handler)| handler)
                        .collect();

                    if matches.is_empty() {
                        event.ack().await.ok();
                        return;
                    }

                    let results = futures_util::future::join_all(matches.iter().map(|handler| handler(&event))).await;
                    let failures: Vec<_> = results.into_iter().filter_map(Result::err).collect();
                    if failures.is_empty() {
                        event.ack().await.ok();
                        return;
                    }
                    for err in &failures {
                        tracing::warn!(routing_key = event.routing_key, error = %err, "event handler failed");
                    }
                    match retry_policy {
                        Some(policy) => {
                            event.retry_or_give_up(policy).await.ok();
                        }
                        None => {
                            event.nack(false).await.ok();
                        }
                    }
                }
            },
            shutdown,
        )
        .await
    }
}
