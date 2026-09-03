//! The consume-and-run loop (`run_executor`), one firing's idempotency-checked execution
//! (`execute`), and the per-`TargetType` router (`dispatch`) that calls into
//! `super::workflow_transition`/`super::webhook`/`super::email`/`super::steps` — see `super`'s
//! doc comment for why `workflow_transition`/`bulk_query_action` call back over HTTP instead of
//! linking `metap-crud` directly.

use std::future::Future;

use metap_cron::{CronJobDuePayload, RunStatus, TargetType, ROUTING_KEY};
use metap_infra::{run_resilient_consumer, EventBus};
use serde_json::Value;
use sqlx::PgPool;

use super::config::ExecutorConfig;
use super::email::run_email;
use super::steps::run_steps;
use super::webhook::run_webhook;
use super::workflow_transition::{run_bulk_query_action, run_workflow_transition};

pub const QUEUE: &str = "cron.executor";

/// Runs the consume-execute loop until `shutdown` resolves — a thin wrapper around
/// `metap_infra::run_resilient_consumer` (reconnects with backoff on any connection loss,
/// instead of requiring a full process restart the way this function used to) supplying the
/// executor-specific handler (parse `cron.job.due`, run it, ack — or nack a malformed payload
/// to the dead-letter queue).
pub async fn run_executor<B, F, Fut>(
    connect: F,
    pool: &PgPool,
    http: &reqwest::Client,
    config: &ExecutorConfig,
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
        // No retry policy here — a failed job execution already has its own DB-scheduled
        // backoff (`ExecutorConfig`'s `max_attempts`/`retry_backoff_seconds`,
        // `metap_cron::finish_run_with_retry`/`claim_due_retries`), so a *second*,
        // message-level retry on top of it isn't needed for this consumer.
        None,
        connect,
        |event| async move {
            match serde_json::from_value::<CronJobDuePayload>(event.payload.clone()) {
                Ok(payload) => {
                    execute(pool, http, config, &payload).await;
                    event.ack().await.ok();
                }
                Err(err) => {
                    tracing::warn!(error = %err, "malformed cron.job.due payload, routed to dead-letter queue");
                    event.nack(false).await.ok();
                }
            }
        },
        shutdown,
    )
    .await
}

/// Runs one job to completion and records its result — shared by the outbox-consuming loop
/// above and the ticker's `Direct`-dispatch path (`ticker.rs`), so "how a job actually gets
/// executed" has exactly one implementation regardless of which `DispatchMode` got it here.
///
/// Idempotency check first: `cron.job.due` is at-least-once (a crash between `dispatch()`
/// finishing and the message's `ack` landing redelivers it), and `webhook`/`bulk_query_action`
/// have no way to detect a duplicate call on their own — `AUDIT_2.md` found this could silently
/// double-fire an external webhook or re-apply a bulk action to every already-matched record.
/// `run_id`'s `cron_job_runs.status` starts `enqueued` and only ever leaves that state once via
/// `finish_run`/`finish_run_with_retry` at the end of this function, so a status that's already
/// `success`/`failed` means this exact firing already ran to completion — skip re-dispatching
/// and let the caller ack the (now known-duplicate) delivery as normal.
/// Wrapped in a fresh **root** trace context (audit 04 B#5): this process consumes from RabbitMQ,
/// not from an HTTP request, so `metap_runtime::trace_context::current()` is `None` here unless
/// something establishes one — and without it, `attach_trace_context` on the outbound callbacks
/// below is silently a no-op, which is exactly the state the audit found. `from_headers` with no
/// `traceparent` mints a new trace id, so every job run becomes its own trace root that the
/// `workflow_transition`/`bulk_query_action`/`webhook` calls it makes then propagate onward — a
/// record written by a cron job is traceable back to the run that caused it.
pub async fn execute(pool: &PgPool, http: &reqwest::Client, config: &ExecutorConfig, payload: &CronJobDuePayload) {
    let trace_ctx = metap_runtime::trace_context::from_headers(&Default::default());
    metap_runtime::trace_context::scope(trace_ctx, execute_traced(pool, http, config, payload)).await
}

async fn execute_traced(pool: &PgPool, http: &reqwest::Client, config: &ExecutorConfig, payload: &CronJobDuePayload) {
    match metap_cron::run_status(pool, payload.run_id).await {
        Ok(Some(RunStatus::Success)) | Ok(Some(RunStatus::Failed)) | Ok(Some(RunStatus::Waiting)) => {
            tracing::warn!(
                run_id = %payload.run_id,
                job_id = %payload.job_id,
                "cron job run already completed or waiting, skipping duplicate dispatch (redelivered message)"
            );
            return;
        }
        Ok(_) => {}
        Err(err) => {
            tracing::error!(run_id = %payload.run_id, error = %err, "failed to check cron job run status, proceeding anyway");
        }
    }

    if let Err(err) = metap_cron::start_run(pool, payload.run_id).await {
        tracing::error!(run_id = %payload.run_id, error = %err, "failed to mark cron job run started");
    }

    let outcome = dispatch(pool, http, config, payload).await;
    let (status, error, summary) = match outcome {
        Ok(DispatchOutcome::Completed(summary)) => (RunStatus::Success, None, Some(summary)),
        // `run_steps` already wrote `cron_job_runs.status = "waiting"` (via `pause_workflow_run`)
        // before returning this — nothing left to record here, and calling
        // `finish_run_with_retry` would incorrectly mark the run finished while it's still
        // durably paused.
        Ok(DispatchOutcome::Waiting) => {
            tracing::info!(job_id = %payload.job_id, run_id = %payload.run_id, "cron job chain paused on wait_event");
            return;
        }
        Err(err) => (RunStatus::Failed, Some(err.to_string()), None),
    };

    if status == RunStatus::Failed {
        tracing::warn!(job_id = %payload.job_id, run_id = %payload.run_id, error = ?error, "cron job execution failed");
    } else {
        tracing::info!(job_id = %payload.job_id, run_id = %payload.run_id, "cron job executed");
    }

    if let Err(err) = metap_cron::finish_run_with_retry(pool, payload, status, error.as_deref(), summary).await {
        tracing::error!(run_id = %payload.run_id, error = %err, "failed to record cron job run result");
    }
}

/// What a `dispatch()` call actually produced — `Completed` for every target type that finishes
/// within one dispatch (which is all of them except `Steps` hitting a `wait_event` step), leaving
/// `Waiting` a `Steps`-only outcome. Kept as its own type rather than folding `Value::Null` into
/// the success case — that would make a paused chain indistinguishable from an activity that
/// genuinely returned no summary.
pub(crate) enum DispatchOutcome {
    Completed(Value),
    Waiting,
}

async fn dispatch(
    pool: &PgPool,
    http: &reqwest::Client,
    config: &ExecutorConfig,
    payload: &CronJobDuePayload,
) -> anyhow::Result<DispatchOutcome> {
    let Some(target_type) = TargetType::parse(&payload.target_type) else {
        anyhow::bail!("unknown target_type {:?}", payload.target_type);
    };
    match target_type {
        TargetType::WorkflowTransition => run_workflow_transition(http, config, &payload.target_config)
            .await
            .map(DispatchOutcome::Completed),
        TargetType::BulkQueryAction => run_bulk_query_action(http, config, &payload.target_config)
            .await
            .map(DispatchOutcome::Completed),
        TargetType::Webhook => run_webhook(&config.webhook, payload.job_id, payload.run_id, &payload.target_config)
            .await
            .map(DispatchOutcome::Completed),
        TargetType::Email => run_email(
            &config.smtp,
            payload.trigger_entity.as_deref(),
            payload.trigger_record_id,
            &payload.target_config,
        )
        .await
        .map(DispatchOutcome::Completed),
        TargetType::Steps => run_steps(pool, http, config, payload).await,
        TargetType::WaitEvent => {
            anyhow::bail!("\"wait_event\" is not a valid top-level targetType, only a step inside a \"steps\" chain")
        }
    }
}
