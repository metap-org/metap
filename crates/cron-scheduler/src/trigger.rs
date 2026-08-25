//! Consumes every event on the shared exchange (`ROUTING_KEY = "#"`, the same catch-all
//! pattern `notification-worker` uses a narrower version of) and dispatches any `cron_jobs` row
//! whose trigger matches — `trigger_type = "on_transition"` for a `<entity>.workflow.transitioned`
//! event (`docs/features/02-workflow-engine.md` Increment 1), or `trigger_type =
//! "on_record_event"` for a `<entity>.record.{created,updated,deleted}` event
//! (`docs/roadmap/38-generic-record-event-triggers.md`, generalizing the former to cover every
//! record lifecycle event, not just workflow transitions). A routing key that matches neither
//! shape (e.g. `cron.job.due`, meant for `executor::run_executor`'s queue, or any future topic
//! this listener doesn't know about) is acked silently — receiving it is expected under a
//! catch-all subscription, not a data error worth a dead-letter. Mirrors `executor::run_executor`'s
//! loop shape.

use std::future::Future;
use std::time::Duration;

use metap_infra::{run_resilient_consumer, ConsumedEvent, EventBus, RetryPolicy};
use sqlx::PgPool;
use uuid::Uuid;

use crate::executor::{execute, ExecutorConfig};

pub const QUEUE: &str = "cron.workflow-trigger";
pub const ROUTING_KEY: &str = "#";

/// A trigger-match attempt failing is (almost always) a transient DB error, not a permanent
/// one — worth a few backed-off retries before giving up, unlike a malformed payload (never
/// worth retrying, see `reject_malformed`). 5s/30s/2m: enough spacing that a brief DB hiccup
/// clears well within the first tier, capped low enough that a genuinely broken DB doesn't sit
/// retrying for hours before landing in the dead-letter queue.
fn retry_policy() -> RetryPolicy {
    RetryPolicy {
        delays: vec![
            Duration::from_secs(5),
            Duration::from_secs(30),
            Duration::from_secs(120),
        ],
    }
}

/// What kind of trigger-worthy event a routing key names — `None` for anything else (not an
/// error, just not this listener's concern).
enum Topic<'a> {
    Transitioned { entity: &'a str },
    RecordEvent { entity: &'a str, event: &'static str },
}

fn classify_topic(routing_key: &str) -> Option<Topic<'_>> {
    if let Some(entity) = routing_key.strip_suffix(".workflow.transitioned") {
        return Some(Topic::Transitioned { entity });
    }
    for (suffix, event) in [
        (".record.created", "created"),
        (".record.updated", "updated"),
        (".record.deleted", "deleted"),
    ] {
        if let Some(entity) = routing_key.strip_suffix(suffix) {
            return Some(Topic::RecordEvent { entity, event });
        }
    }
    None
}

/// Runs the consume-dispatch loop until `shutdown` resolves — a thin wrapper around
/// `metap_infra::run_resilient_consumer` (reconnects with backoff on any connection loss,
/// instead of requiring a full process restart the way this function used to). Note this and
/// `executor::run_executor` each reconnect independently (separate `RabbitEventBus` connections)
/// rather than sharing one — a genuine improvement over the previous single-shared-bus setup,
/// since one consumer reconnecting no longer interrupts the other's in-flight stream.
pub async fn run_trigger_listener<B, F, Fut>(
    connect: F,
    pool: &PgPool,
    http: &reqwest::Client,
    executor_config: &ExecutorConfig,
    shutdown: impl Future<Output = ()>,
) -> anyhow::Result<()>
where
    B: EventBus,
    F: Fn() -> Fut,
    Fut: Future<Output = anyhow::Result<B>>,
{
    let policy = retry_policy();
    run_resilient_consumer(
        QUEUE,
        ROUTING_KEY,
        Some(&policy),
        connect,
        |event| {
            let policy = policy.clone();
            async move {
                match classify_topic(&event.routing_key) {
                    Some(Topic::Transitioned { entity }) => {
                        let entity = entity.to_string();
                        match (parse_tenant_id(&event), parse_record_id(&event)) {
                            (Some(tenant_id), Some(record_id)) => {
                                let action = event.payload.get("action").and_then(|v| v.as_str());
                                match action {
                                    Some(action) => {
                                        dispatch_transition(
                                            pool,
                                            http,
                                            executor_config,
                                            &policy,
                                            &event,
                                            tenant_id,
                                            &entity,
                                            action,
                                            record_id,
                                        )
                                        .await
                                    }
                                    None => reject_malformed(&event, "missing/invalid `action`").await,
                                }
                            }
                            _ => reject_malformed(&event, "missing/invalid `tenantId`/`recordId`").await,
                        }
                    }
                    Some(Topic::RecordEvent { entity, event: kind }) => {
                        let entity = entity.to_string();
                        match (parse_tenant_id(&event), parse_record_id(&event)) {
                            (Some(tenant_id), Some(record_id)) => {
                                dispatch_record_event(
                                    pool,
                                    http,
                                    executor_config,
                                    &policy,
                                    &event,
                                    tenant_id,
                                    &entity,
                                    kind,
                                    record_id,
                                )
                                .await
                            }
                            _ => reject_malformed(&event, "missing/invalid `tenantId`/`recordId`").await,
                        }
                    }
                    // Not a topic this listener cares about (e.g. `cron.job.due`, meant for the
                    // executor's own queue) — acking is correct here, not a fallback: under a
                    // catch-all subscription, receiving other topics is the expected case.
                    None => {
                        event.ack().await.ok();
                    }
                }
            }
        },
        shutdown,
    )
    .await
}

async fn reject_malformed(event: &ConsumedEvent, reason: &str) {
    tracing::warn!(
        routing_key = event.routing_key,
        reason,
        "malformed trigger-worthy payload, routed to dead-letter queue"
    );
    event.nack(false).await.ok();
}

/// `tenantId` is the one field every trigger-worthy payload carries
/// (`metap_workflow::emit_transitioned`/`emit_created`/`emit_updated`/`emit_deleted` all
/// include it) — shared regardless of which `Topic` the routing key classified as.
fn parse_tenant_id(event: &ConsumedEvent) -> Option<Uuid> {
    let tenant_id = event.payload.get("tenantId")?.as_str()?;
    Uuid::parse_str(tenant_id).ok()
}

/// `recordId` is likewise present on every trigger-worthy payload
/// (`metap_workflow::emit_transitioned`/`emit_created`/`emit_updated`/`emit_deleted` all include
/// it) — threaded through to `CronJobDuePayload::trigger_record_id` so a target (`run_email`/
/// `run_webhook`) can reference which record actually caused the firing.
fn parse_record_id(event: &ConsumedEvent) -> Option<Uuid> {
    let record_id = event.payload.get("recordId")?.as_str()?;
    Uuid::parse_str(record_id).ok()
}

#[allow(clippy::too_many_arguments)]
async fn dispatch_transition(
    pool: &PgPool,
    http: &reqwest::Client,
    executor_config: &ExecutorConfig,
    policy: &RetryPolicy,
    event: &ConsumedEvent,
    tenant_id: Uuid,
    entity: &str,
    action: &str,
    record_id: Uuid,
) {
    let result = metap_cron::dispatch_on_transition_matches(pool, tenant_id, entity, action, record_id).await;
    finish_dispatch(
        pool,
        http,
        executor_config,
        policy,
        event,
        result,
        "on_transition",
        tenant_id,
        entity,
        action,
    )
    .await;
}

#[allow(clippy::too_many_arguments)]
async fn dispatch_record_event(
    pool: &PgPool,
    http: &reqwest::Client,
    executor_config: &ExecutorConfig,
    policy: &RetryPolicy,
    event: &ConsumedEvent,
    tenant_id: Uuid,
    entity: &str,
    record_event: &str,
    record_id: Uuid,
) {
    let result = metap_cron::dispatch_on_record_event_matches(pool, tenant_id, entity, record_event, record_id).await;
    finish_dispatch(
        pool,
        http,
        executor_config,
        policy,
        event,
        result,
        "on_record_event",
        tenant_id,
        entity,
        record_event,
    )
    .await;
}

/// Shared ack/nack + direct-dispatch tail for both trigger kinds — the only thing that differs
/// between them is which `metap_cron::dispatch_on_*_matches` query ran, already done by the
/// caller.
#[allow(clippy::too_many_arguments)]
async fn finish_dispatch(
    pool: &PgPool,
    http: &reqwest::Client,
    executor_config: &ExecutorConfig,
    policy: &RetryPolicy,
    event: &ConsumedEvent,
    result: anyhow::Result<metap_cron::ClaimResult>,
    trigger_kind: &str,
    tenant_id: Uuid,
    entity: &str,
    detail: &str,
) {
    match result {
        Ok(result) => {
            for direct_job in result.direct_jobs {
                let payload = metap_cron::CronJobDuePayload {
                    run_id: direct_job.run_id,
                    job_id: direct_job.job_id,
                    tenant_id: direct_job.tenant_id,
                    target_type: direct_job.target_type,
                    target_config: direct_job.target_config,
                    attempt: direct_job.attempt,
                    max_attempts: direct_job.max_attempts,
                    retry_backoff_seconds: direct_job.retry_backoff_seconds,
                    dispatch_mode: direct_job.dispatch_mode,
                    trigger_record_id: direct_job.trigger_record_id,
                    trigger_entity: direct_job.trigger_entity,
                };
                execute(pool, http, executor_config, &payload).await;
            }
            event.ack().await.ok();
        }
        Err(err) => {
            // Failed before any cron_job_runs row could be written for this firing (e.g. a
            // transient DB error) — retry with backoff (`retry_policy`) instead of the old
            // behavior of dead-lettering on the very first failure: a brief DB hiccup used to
            // permanently lose that trigger evaluation, now it gets a few backed-off chances to
            // clear before giving up.
            tracing::warn!(
                tenant_id = %tenant_id, entity, trigger_kind, detail, error = %err,
                retry_count = event.retry_count(),
                "failed to dispatch trigger matches, retrying with backoff"
            );
            event.retry_or_give_up(policy).await.ok();
        }
    }
}
