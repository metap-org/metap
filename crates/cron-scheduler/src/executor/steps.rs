//! `TargetType::Steps` chain execution — running a fresh chain (`run_steps`), continuing one
//! that paused at a `wait_event` step (`resume_steps`, called by `cron-scheduler::trigger`),
//! and the shared step-range loop (`run_step_range`) both go through. See `TargetType::Steps`/
//! `TargetType::WaitEvent`'s doc comments in `metap-cron` for the increment history.

use metap_cron::{
    advance_workflow_run, fail_workflow_run, finish_run, finish_run_with_retry, finish_workflow_run,
    pause_workflow_run, start_workflow_run, CronJobDuePayload, ResumedWorkflowRun, RunStatus, TargetType,
    WaitEventTargetConfig,
};
use serde::Deserialize;
use serde_json::{json, Value};
use sqlx::PgPool;
use uuid::Uuid;

use super::config::ExecutorConfig;
use super::dispatch::DispatchOutcome;
use super::email::run_email;
use super::webhook::run_webhook;
use super::workflow_transition::{run_bulk_query_action, run_workflow_transition};

#[derive(Deserialize)]
struct Activity {
    #[serde(rename = "targetType")]
    target_type: String,
    #[serde(rename = "targetConfig")]
    target_config: Value,
}

#[derive(Deserialize)]
struct StepsConfig {
    steps: Vec<Activity>,
}

/// Runs `payload.target_config.steps` one after another, in order, within this single dispatch
/// (until it either finishes, fails, or hits a `wait_event` step and pauses — Increment 3).
/// Tracks progress in `workflow_runs` via `start_workflow_run`/`advance_workflow_run`/
/// `finish_workflow_run`/`fail_workflow_run`/`pause_workflow_run` — a step failure stops the
/// chain and fails the whole firing, which the existing retry-with-backoff
/// (`finish_run_with_retry`) then retries from step 0 exactly like any other target type, no
/// special-casing needed here (`resume_steps` below applies the exact same retry-from-0 policy
/// for a failure *after* a resume).
pub(crate) async fn run_steps(
    pool: &PgPool,
    http: &reqwest::Client,
    config: &ExecutorConfig,
    payload: &CronJobDuePayload,
) -> anyhow::Result<DispatchOutcome> {
    let cfg: StepsConfig = serde_json::from_value(payload.target_config.clone())?;
    if cfg.steps.is_empty() {
        anyhow::bail!("steps target_config.steps must not be empty");
    }

    let workflow_run_id = start_workflow_run(
        pool,
        payload.tenant_id,
        payload.job_id,
        payload.run_id,
        cfg.steps.len() as i32,
    )
    .await?;

    run_step_range(pool, http, config, payload, workflow_run_id, &cfg.steps, 0).await
}

/// Runs `steps[start_index..]` in order against an already-existing `workflow_run_id` — shared
/// by `run_steps` (a fresh chain, `start_index = 0`) and `resume_steps` (an already-paused chain
/// picking back up right after its `wait_event` step). Pulled out separately so pausing again on
/// a *second* `wait_event` step later in the same chain works identically whether this is the
/// chain's first run or a resume — the loop doesn't know or care which.
#[allow(clippy::too_many_arguments)]
async fn run_step_range(
    pool: &PgPool,
    http: &reqwest::Client,
    config: &ExecutorConfig,
    payload: &CronJobDuePayload,
    workflow_run_id: Uuid,
    steps: &[Activity],
    start_index: usize,
) -> anyhow::Result<DispatchOutcome> {
    let mut results = Vec::with_capacity(steps.len() - start_index);
    for (offset, step) in steps[start_index..].iter().enumerate() {
        let step_index = (start_index + offset) as i32;

        if let Some(TargetType::WaitEvent) = TargetType::parse(&step.target_type) {
            let wait: WaitEventTargetConfig = serde_json::from_value(step.target_config.clone())
                .map_err(|err| anyhow::anyhow!("step {step_index} (wait_event) has an invalid targetConfig: {err}"))?;
            pause_workflow_run(pool, workflow_run_id, payload.run_id, step_index, &wait).await?;
            tracing::info!(
                run_id = %workflow_run_id, step_index, entity = wait.entity, action = ?wait.action, event = ?wait.event,
                "workflow chain paused at wait_event step"
            );
            return Ok(DispatchOutcome::Waiting);
        }

        let step_result = run_one_step(http, config, payload, step).await;
        match step_result {
            Ok(value) => {
                if let Err(err) = advance_workflow_run(pool, workflow_run_id, step_index, &value).await {
                    tracing::error!(run_id = %workflow_run_id, step_index, error = %err, "failed to record workflow run step progress");
                }
                results.push(value);
            }
            Err(err) => {
                let message = format!("step {step_index} ({}) failed: {err}", step.target_type);
                if let Err(record_err) = fail_workflow_run(pool, workflow_run_id, step_index, &message).await {
                    tracing::error!(run_id = %workflow_run_id, step_index, error = %record_err, "failed to record workflow run step failure");
                }
                anyhow::bail!(message);
            }
        }
    }

    if let Err(err) = finish_workflow_run(pool, workflow_run_id).await {
        tracing::error!(run_id = %workflow_run_id, error = %err, "failed to mark workflow run finished");
    }
    Ok(DispatchOutcome::Completed(json!({ "steps": results })))
}

/// Continues a `TargetType::Steps` chain a `wait_event` step previously paused —
/// `cron-scheduler::trigger`'s listener calls this for every `ResumedWorkflowRun`
/// `metap_cron::dispatch_on_wait_event_transition_matches`/`dispatch_on_wait_event_record_matches`
/// returns. Reconstructs a `CronJobDuePayload` from the job/run row `resume_matching` (in
/// `metap-cron`) already joined in, since the resume path doesn't have (and Increment 2's
/// `dispatch_claimed` doc comment already accepts this gap for a plain retry too) the *original*
/// firing's trigger context — `trigger_record_id`/`trigger_entity` here are the **resuming**
/// event's instead, so any step running after the wait references what actually caused the chain
/// to continue. On success, closes the run out directly (`finish_run`, no retry needed). On
/// failure, goes through `finish_run_with_retry` exactly like a first-run failure — the chain
/// retries from step 0 on the next attempt, not from wherever it paused; there's no
/// resume-aware retry here, deliberately (`TargetType::WaitEvent`'s doc comment).
pub async fn resume_steps(
    pool: &PgPool,
    http: &reqwest::Client,
    config: &ExecutorConfig,
    resumed: &ResumedWorkflowRun,
) {
    let payload = CronJobDuePayload {
        run_id: resumed.cron_job_run_id,
        job_id: resumed.job_id,
        tenant_id: resumed.tenant_id,
        target_type: TargetType::Steps.as_str().to_string(),
        target_config: resumed.target_config.clone(),
        attempt: resumed.attempt,
        max_attempts: resumed.max_attempts,
        retry_backoff_seconds: resumed.retry_backoff_seconds,
        dispatch_mode: resumed.dispatch_mode.clone(),
        trigger_record_id: Some(resumed.resuming_record_id),
        trigger_entity: Some(resumed.resuming_entity.clone()),
    };
    let cfg: StepsConfig = match serde_json::from_value(payload.target_config.clone()) {
        Ok(cfg) => cfg,
        Err(err) => {
            tracing::error!(run_id = %resumed.cron_job_run_id, error = %err, "failed to parse steps target_config on resume");
            let _ = finish_run_with_retry(pool, &payload, RunStatus::Failed, Some(&err.to_string()), None).await;
            return;
        }
    };

    let outcome = run_step_range(
        pool,
        http,
        config,
        &payload,
        resumed.workflow_run_id,
        &cfg.steps,
        resumed.resume_from_step_index as usize,
    )
    .await;

    match outcome {
        Ok(DispatchOutcome::Completed(summary)) => {
            tracing::info!(job_id = %payload.job_id, run_id = %payload.run_id, "cron job chain resumed and completed");
            if let Err(err) = finish_run(pool, payload.run_id, RunStatus::Success, None, Some(summary)).await {
                tracing::error!(run_id = %payload.run_id, error = %err, "failed to record resumed cron job run result");
            }
        }
        // Another `wait_event` step later in the same chain — already paused again by
        // `run_step_range`/`pause_workflow_run`, nothing left to do here.
        Ok(DispatchOutcome::Waiting) => {
            tracing::info!(job_id = %payload.job_id, run_id = %payload.run_id, "cron job chain paused again on a later wait_event");
        }
        Err(err) => {
            tracing::warn!(job_id = %payload.job_id, run_id = %payload.run_id, error = %err, "resumed cron job chain failed");
            if let Err(record_err) =
                finish_run_with_retry(pool, &payload, RunStatus::Failed, Some(&err.to_string()), None).await
            {
                tracing::error!(run_id = %payload.run_id, error = %record_err, "failed to record resumed cron job run result");
            }
        }
    }
}

/// Runs one step's activity, reusing the exact same `run_*` functions the non-chained target
/// types dispatch through — a step is just an `Activity`'s `(targetType, targetConfig)` run in
/// isolation. `chain` is the overall `"steps"` job's own `CronJobDuePayload` (not a per-step
/// one — a step has no `run_id`/trigger context of its own): `run_webhook`/`run_email` get the
/// chain's real `job_id`/`run_id`/trigger fields, same as they'd see running as a standalone
/// (non-chained) job for that same firing.
async fn run_one_step(
    http: &reqwest::Client,
    config: &ExecutorConfig,
    chain: &CronJobDuePayload,
    step: &Activity,
) -> anyhow::Result<Value> {
    match TargetType::parse(&step.target_type) {
        Some(TargetType::WorkflowTransition) => run_workflow_transition(http, config, &step.target_config).await,
        Some(TargetType::BulkQueryAction) => run_bulk_query_action(http, config, &step.target_config).await,
        Some(TargetType::Webhook) => run_webhook(http, chain.job_id, chain.run_id, &step.target_config).await,
        Some(TargetType::Email) => {
            run_email(
                &config.smtp,
                chain.trigger_entity.as_deref(),
                chain.trigger_record_id,
                &step.target_config,
            )
            .await
        }
        Some(TargetType::Steps) => {
            anyhow::bail!("nested \"steps\" targetType is not supported inside a step")
        }
        // `run_step_range` intercepts `wait_event` steps before ever calling `run_one_step` — a
        // step reaching here as `WaitEvent` means that interception was skipped, a bug in the
        // caller, not a config error worth a normal "unsupported" message.
        Some(TargetType::WaitEvent) => {
            anyhow::bail!("wait_event step reached run_one_step — should have been intercepted by run_step_range")
        }
        None => anyhow::bail!("unknown targetType {:?} in step", step.target_type),
    }
}
