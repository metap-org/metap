//! `workflow_runs` — progress/audit state for one `TargetType::Steps` firing
//! (`docs/features/02-workflow-engine.md` Increment 2), plus the `wait_event` durable-pause/
//! resume bookkeeping added in Increment 3.

use sqlx::{PgPool, Row};
use uuid::Uuid;

use crate::model::{ResumedWorkflowRun, RunStatus, WaitEventTargetConfig, WorkflowRun, WorkflowRunStatus};

fn workflow_run_from_row(row: &sqlx::postgres::PgRow) -> anyhow::Result<WorkflowRun> {
    Ok(WorkflowRun {
        id: row.try_get("id")?,
        tenant_id: row.try_get("tenant_id")?,
        job_id: row.try_get("job_id")?,
        cron_job_run_id: row.try_get("cron_job_run_id")?,
        status: row.try_get("status")?,
        current_step_index: row.try_get("current_step_index")?,
        total_steps: row.try_get("total_steps")?,
        context: row.try_get("context")?,
        error: row.try_get("error")?,
        wait_entity: row.try_get("wait_entity")?,
        wait_action: row.try_get("wait_action")?,
        wait_record_event: row.try_get("wait_record_event")?,
        started_at: row.try_get("started_at")?,
        finished_at: row.try_get("finished_at")?,
        created_at: row.try_get("created_at")?,
    })
}

/// Creates the `workflow_runs` row for a `TargetType::Steps` firing — called once at the start
/// of `cron-scheduler::executor::run_steps`, before the first step executes. One row per
/// `cron_job_runs` row (enforced by `workflow_runs_cron_job_run_idx`'s uniqueness) — a redelivered
/// `cron.job.due` message re-entering `run_steps` for an already-completed `cron_job_run_id`
/// never gets this far (`execute`'s existing `run_status` idempotency check short-circuits
/// first), so this never needs an upsert.
pub async fn start_workflow_run(
    pool: &PgPool,
    tenant_id: Uuid,
    job_id: Uuid,
    cron_job_run_id: Uuid,
    total_steps: i32,
) -> anyhow::Result<Uuid> {
    let row = sqlx::query(
        "INSERT INTO workflow_runs (tenant_id, job_id, cron_job_run_id, status, total_steps) \
         VALUES ($1, $2, $3, $4, $5) RETURNING id",
    )
    .bind(tenant_id)
    .bind(job_id)
    .bind(cron_job_run_id)
    .bind(WorkflowRunStatus::Running.as_str())
    .bind(total_steps)
    .fetch_one(pool)
    .await?;
    Ok(row.try_get("id")?)
}

/// Records step `step_index`'s successful result and advances `current_step_index` past it —
/// called after each step in `run_steps` completes. `result` is merged into `context` under key
/// `"step_<index>"` (audit trail only, per `TargetType::Steps`'s doc comment — no later step
/// reads it back).
pub async fn advance_workflow_run(
    pool: &PgPool,
    run_id: Uuid,
    step_index: i32,
    result: &serde_json::Value,
) -> anyhow::Result<()> {
    sqlx::query("UPDATE workflow_runs SET current_step_index = $1, context = jsonb_set(context, $2, $3) WHERE id = $4")
        .bind(step_index + 1)
        .bind(vec![format!("step_{step_index}")])
        .bind(result)
        .bind(run_id)
        .execute(pool)
        .await?;
    Ok(())
}

/// Marks the whole chain `Success` once every step has completed — called at the end of
/// `run_steps`.
pub async fn finish_workflow_run(pool: &PgPool, run_id: Uuid) -> anyhow::Result<()> {
    sqlx::query("UPDATE workflow_runs SET status = $1, finished_at = now() WHERE id = $2")
        .bind(WorkflowRunStatus::Success.as_str())
        .bind(run_id)
        .execute(pool)
        .await?;
    Ok(())
}

/// Marks the chain `Failed` at `step_index` (the step that failed — `current_step_index` is left
/// pointing at it rather than advanced past it, so it's visible which step stopped the chain).
pub async fn fail_workflow_run(pool: &PgPool, run_id: Uuid, step_index: i32, error: &str) -> anyhow::Result<()> {
    sqlx::query(
        "UPDATE workflow_runs SET status = $1, current_step_index = $2, error = $3, finished_at = now() \
         WHERE id = $4",
    )
    .bind(WorkflowRunStatus::Failed.as_str())
    .bind(step_index)
    .bind(error)
    .bind(run_id)
    .execute(pool)
    .await?;
    Ok(())
}

/// Looks up the `workflow_runs` row for a given `cron_job_runs.id` — lets an operator inspect
/// step-level progress for one firing via `GET /admin/cron-jobs/{jobId}/runs/{runId}/workflow-run`
/// alongside the plain `CronJobRun` row `list_job_runs` already exposes. `Ok(None)` both when the
/// run hasn't reached `run_steps` yet (shouldn't normally be observed — `start_workflow_run`
/// happens synchronously before any step runs) and when the job isn't a `"steps"` job at all;
/// callers can't distinguish those, which is fine — both mean "nothing to show here".
pub async fn get_workflow_run_by_cron_job_run(
    pool: &PgPool,
    tenant_id: Uuid,
    cron_job_run_id: Uuid,
) -> anyhow::Result<Option<WorkflowRun>> {
    let row = sqlx::query("SELECT * FROM workflow_runs WHERE tenant_id = $1 AND cron_job_run_id = $2")
        .bind(tenant_id)
        .bind(cron_job_run_id)
        .fetch_optional(pool)
        .await?;
    row.as_ref().map(workflow_run_from_row).transpose()
}

/// Pauses a `TargetType::Steps` chain at a `wait_event` step (Increment 3) — called by
/// `cron-scheduler::executor::run_steps` in place of `advance_workflow_run` when the step it just
/// reached is a wait rather than an activity. Marks **both** `workflow_runs` (`status =
/// "waiting"`, `current_step_index` left pointing at the wait step itself — same "points at the
/// step in progress" convention `fail_workflow_run` uses, not advanced past it since it hasn't
/// completed) and `cron_job_runs` (`status = "waiting"`) in one transaction, so a reader never
/// observes one paused and the other not. `resume_matching` below is the only thing that ever
/// flips a row this wrote back out of `"waiting"`.
pub async fn pause_workflow_run(
    pool: &PgPool,
    workflow_run_id: Uuid,
    cron_job_run_id: Uuid,
    step_index: i32,
    wait: &WaitEventTargetConfig,
) -> anyhow::Result<()> {
    let mut tx = pool.begin().await?;
    sqlx::query(
        "UPDATE workflow_runs SET status = $1, current_step_index = $2, wait_entity = $3, wait_action = $4, \
         wait_record_event = $5 WHERE id = $6",
    )
    .bind(WorkflowRunStatus::Waiting.as_str())
    .bind(step_index)
    .bind(&wait.entity)
    .bind(&wait.action)
    .bind(&wait.event)
    .bind(workflow_run_id)
    .execute(&mut *tx)
    .await?;
    sqlx::query("UPDATE cron_job_runs SET status = $1 WHERE id = $2")
        .bind(RunStatus::Waiting.as_str())
        .bind(cron_job_run_id)
        .execute(&mut *tx)
        .await?;
    tx.commit().await?;
    Ok(())
}

/// Shared by `dispatch_on_wait_event_transition_matches`/`dispatch_on_wait_event_record_matches`
/// below — finds every `workflow_runs` row `waiting` on `(tenant_id, entity, detail)` (`detail`
/// is an `action` or a record `event`, `wait_column` says which), flips each to `"running"`
/// **inside the same `FOR UPDATE SKIP LOCKED` transaction** it was matched in (the concurrency
/// guard: a second event matching the same wait between the SELECT and the UPDATE simply skips a
/// row the first transaction already locked, same shape as `claim_due_jobs`), and joins to
/// `cron_jobs`/`cron_job_runs` for everything `resume_steps` needs to actually continue the
/// chain without a second round-trip.
async fn resume_matching(
    pool: &PgPool,
    tenant_id: Uuid,
    entity: &str,
    wait_column: &str,
    detail: &str,
    resuming_record_id: Uuid,
) -> anyhow::Result<Vec<ResumedWorkflowRun>> {
    let mut tx = pool.begin().await?;
    let query = format!(
        "SELECT w.id AS workflow_run_id, w.cron_job_run_id, w.job_id, w.tenant_id, w.current_step_index, \
         j.target_config, j.dispatch_mode, j.max_attempts, j.retry_backoff_seconds, r.attempt \
         FROM workflow_runs w \
         JOIN cron_jobs j ON j.id = w.job_id \
         JOIN cron_job_runs r ON r.id = w.cron_job_run_id \
         WHERE w.tenant_id = $1 AND w.status = 'waiting' AND w.wait_entity = $2 AND w.{wait_column} = $3 \
         FOR UPDATE OF w SKIP LOCKED"
    );
    let rows = sqlx::query(&query)
        .bind(tenant_id)
        .bind(entity)
        .bind(detail)
        .fetch_all(&mut *tx)
        .await?;

    let mut resumed = Vec::with_capacity(rows.len());
    for row in &rows {
        let workflow_run_id: Uuid = row.try_get("workflow_run_id")?;
        sqlx::query("UPDATE workflow_runs SET status = $1 WHERE id = $2")
            .bind(WorkflowRunStatus::Running.as_str())
            .bind(workflow_run_id)
            .execute(&mut *tx)
            .await?;
        let current_step_index: i32 = row.try_get("current_step_index")?;
        resumed.push(ResumedWorkflowRun {
            workflow_run_id,
            cron_job_run_id: row.try_get("cron_job_run_id")?,
            job_id: row.try_get("job_id")?,
            tenant_id: row.try_get("tenant_id")?,
            resume_from_step_index: current_step_index + 1,
            target_config: row.try_get("target_config")?,
            dispatch_mode: row.try_get("dispatch_mode")?,
            max_attempts: row.try_get("max_attempts")?,
            retry_backoff_seconds: row.try_get("retry_backoff_seconds")?,
            attempt: row.try_get("attempt")?,
            resuming_record_id,
            resuming_entity: entity.to_string(),
        });
    }
    tx.commit().await?;
    Ok(resumed)
}

/// Resumes every `wait_event` step waiting on `(tenant_id, entity, action)` — the `on_transition`
/// counterpart to `dispatch_on_transition_matches`, called by `cron-scheduler::trigger`'s
/// listener for the same `<entity>.workflow.transitioned` events, alongside (not instead of) that
/// function — a single transition can both fire brand-new `on_transition` jobs and resume paused
/// chains waiting on it.
pub async fn dispatch_on_wait_event_transition_matches(
    pool: &PgPool,
    tenant_id: Uuid,
    entity: &str,
    action: &str,
    record_id: Uuid,
) -> anyhow::Result<Vec<ResumedWorkflowRun>> {
    resume_matching(pool, tenant_id, entity, "wait_action", action, record_id).await
}

/// Resumes every `wait_event` step waiting on `(tenant_id, entity, event)` — the
/// `on_record_event` counterpart, called alongside `dispatch_on_record_event_matches`.
pub async fn dispatch_on_wait_event_record_matches(
    pool: &PgPool,
    tenant_id: Uuid,
    entity: &str,
    event: &str,
    record_id: Uuid,
) -> anyhow::Result<Vec<ResumedWorkflowRun>> {
    resume_matching(pool, tenant_id, entity, "wait_record_event", event, record_id).await
}
