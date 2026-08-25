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

use metap_infra::{run_resilient_consumer, ConsumedEvent, EventBus};
use sqlx::PgPool;
use uuid::Uuid;

use crate::executor::{execute, ExecutorConfig};

pub const QUEUE: &str = "cron.workflow-trigger";
pub const ROUTING_KEY: &str = "#";

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
    run_resilient_consumer(
        QUEUE,
        ROUTING_KEY,
        connect,
        |event| async move {
            match classify_topic(&event.routing_key) {
                Some(Topic::Transitioned { entity }) => {
                    let entity = entity.to_string();
                    match parse_tenant_id(&event) {
                        Some(tenant_id) => {
                            let action = event.payload.get("action").and_then(|v| v.as_str());
                            match action {
                                Some(action) => {
                                    dispatch_transition(pool, http, executor_config, &event, tenant_id, &entity, action)
                                        .await
                                }
                                None => reject_malformed(&event, "missing/invalid `action`").await,
                            }
                        }
                        None => reject_malformed(&event, "missing/invalid `tenantId`").await,
                    }
                }
                Some(Topic::RecordEvent { entity, event: kind }) => {
                    let entity = entity.to_string();
                    match parse_tenant_id(&event) {
                        Some(tenant_id) => {
                            dispatch_record_event(pool, http, executor_config, &event, tenant_id, &entity, kind).await
                        }
                        None => reject_malformed(&event, "missing/invalid `tenantId`").await,
                    }
                }
                // Not a topic this listener cares about (e.g. `cron.job.due`, meant for the
                // executor's own queue) — acking is correct here, not a fallback: under a
                // catch-all subscription, receiving other topics is the expected case.
                None => {
                    event.ack().await.ok();
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

async fn dispatch_transition(
    pool: &PgPool,
    http: &reqwest::Client,
    executor_config: &ExecutorConfig,
    event: &ConsumedEvent,
    tenant_id: Uuid,
    entity: &str,
    action: &str,
) {
    let result = metap_cron::dispatch_on_transition_matches(pool, tenant_id, entity, action).await;
    finish_dispatch(
        pool,
        http,
        executor_config,
        event,
        result,
        "on_transition",
        tenant_id,
        entity,
        action,
    )
    .await;
}

async fn dispatch_record_event(
    pool: &PgPool,
    http: &reqwest::Client,
    executor_config: &ExecutorConfig,
    event: &ConsumedEvent,
    tenant_id: Uuid,
    entity: &str,
    record_event: &str,
) {
    let result = metap_cron::dispatch_on_record_event_matches(pool, tenant_id, entity, record_event).await;
    finish_dispatch(
        pool,
        http,
        executor_config,
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
                };
                execute(pool, http, executor_config, &payload).await;
            }
            event.ack().await.ok();
        }
        Err(err) => {
            // Failed before any cron_job_runs row could be written for this firing (e.g. a
            // transient DB error) — nothing tracks a retry for it the way
            // `finish_run_with_retry` tracks an execution failure, so dead-letter it instead of
            // silently dropping the trigger evaluation (ack) or hot-looping redelivery against a
            // possibly still-struggling DB (nack requeue:true).
            tracing::error!(
                tenant_id = %tenant_id, entity, trigger_kind, detail, error = %err,
                "failed to dispatch trigger matches, routed to dead-letter queue"
            );
            event.nack(false).await.ok();
        }
    }
}
