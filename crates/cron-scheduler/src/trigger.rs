//! Consumes `#.workflow.transitioned` (the same routing key `notification-worker` subscribes
//! to, under a different queue name — AMQP work-queue semantics mean two distinct queue names
//! both get every matching message, so this consumer and notification-worker never compete for
//! the same delivery) and dispatches any `trigger_type = "on_transition"` `cron_jobs` row whose
//! `trigger_config` matches the transitioned entity/action
//! (`docs/features/02-workflow-engine.md` Increment 1). Mirrors `executor::run_executor`'s loop
//! shape.

use futures_util::StreamExt;
use metap_infra::{ConsumedEvent, EventBus};
use sqlx::PgPool;
use uuid::Uuid;

use crate::executor::{execute, ExecutorConfig};

pub const QUEUE: &str = "cron.workflow-trigger";
pub const ROUTING_KEY: &str = "#.workflow.transitioned";

pub async fn run_trigger_listener(
    bus: &impl EventBus,
    pool: &PgPool,
    http: &reqwest::Client,
    executor_config: &ExecutorConfig,
    shutdown: impl std::future::Future<Output = ()>,
) -> anyhow::Result<()> {
    let mut events = bus.subscribe(QUEUE, ROUTING_KEY).await?;
    let mut shutdown = std::pin::pin!(shutdown);

    loop {
        tokio::select! {
            biased;
            _ = &mut shutdown => {
                tracing::info!("shutdown signal received, exiting trigger listener");
                return Ok(());
            }
            event = events.next() => {
                let Some(event) = event else {
                    anyhow::bail!("event stream closed unexpectedly (bus disconnected?)");
                };
                match parse_transition(&event) {
                    Some((tenant_id, entity, action)) => {
                        match dispatch(pool, http, executor_config, tenant_id, &entity, &action).await {
                            Ok(()) => {
                                event.ack().await.ok();
                            }
                            Err(err) => {
                                // Failed before any cron_job_runs row could be written for this
                                // firing (e.g. a transient DB error) — nothing tracks a retry for
                                // it the way `finish_run_with_retry` tracks an execution failure,
                                // so dead-letter it instead of silently dropping the trigger
                                // evaluation (ack) or hot-looping redelivery against a possibly
                                // still-struggling DB (nack requeue:true).
                                tracing::error!(
                                    tenant_id = %tenant_id, entity, action, error = %err,
                                    "failed to dispatch on_transition matches, routed to dead-letter queue"
                                );
                                event.nack(false).await.ok();
                            }
                        }
                    }
                    None => {
                        tracing::warn!(
                            routing_key = event.routing_key,
                            "malformed workflow.transitioned payload (missing/invalid tenantId or action), \
                             routed to dead-letter queue"
                        );
                        event.nack(false).await.ok();
                    }
                }
            }
        }
    }
}

/// Entity name comes from the routing key (`<entity>.workflow.transitioned`, same convention
/// `notification-worker::notify` already relies on); `tenantId`/`action` come from the payload
/// (`metap_workflow::emit_transitioned`).
fn parse_transition(event: &ConsumedEvent) -> Option<(Uuid, String, String)> {
    let entity = event.routing_key.strip_suffix(".workflow.transitioned")?.to_string();
    let tenant_id = event.payload.get("tenantId")?.as_str()?;
    let tenant_id = Uuid::parse_str(tenant_id).ok()?;
    let action = event.payload.get("action")?.as_str()?.to_string();
    Some((tenant_id, entity, action))
}

async fn dispatch(
    pool: &PgPool,
    http: &reqwest::Client,
    executor_config: &ExecutorConfig,
    tenant_id: Uuid,
    entity: &str,
    action: &str,
) -> anyhow::Result<()> {
    let result = metap_cron::dispatch_on_transition_matches(pool, tenant_id, entity, action).await?;
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
    Ok(())
}
