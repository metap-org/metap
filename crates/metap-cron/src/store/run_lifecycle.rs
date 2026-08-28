//! One `cron_job_runs` row's lifecycle after it's been claimed/dispatched: check its status
//! (idempotency), mark it started, and record its final outcome — including scheduling a
//! backed-off retry row on failure (`docs/features/02-workflow-engine.md` Increment 1).

use chrono::Utc;
use sqlx::{PgPool, Row};
use uuid::Uuid;

use crate::model::{CronJobDuePayload, RunStatus};

/// `retry_backoff_seconds * 2^(attempt - 1)` — attempt 1 failing schedules attempt 2 after
/// exactly `retry_backoff_seconds`, attempt 2 failing schedules attempt 3 after
/// `2 * retry_backoff_seconds`, and so on. `attempt` is the attempt that just failed (1-based).
fn backoff_delay(retry_backoff_seconds: i32, attempt: i32) -> chrono::Duration {
    let exponent = (attempt - 1).max(0);
    let seconds = i64::from(retry_backoff_seconds).saturating_mul(1i64 << exponent.min(30));
    chrono::Duration::seconds(seconds)
}

/// The `cron_job_runs.status` a run is currently at, or `None` if `run_id` doesn't exist —
/// `execute`'s idempotency check before actually dispatching (`AUDIT_2.md`: a redelivered
/// `cron.job.due` message, e.g. the process crashing between `dispatch()` finishing and the
/// message's `ack` landing, used to re-run `webhook`/`bulk_query_action` a second time with
/// nothing to detect the run had already completed).
pub async fn run_status(pool: &PgPool, run_id: Uuid) -> anyhow::Result<Option<RunStatus>> {
    let row = sqlx::query("SELECT status FROM cron_job_runs WHERE id = $1")
        .bind(run_id)
        .fetch_optional(pool)
        .await?;
    Ok(row.and_then(|r| RunStatus::parse(r.get::<String, _>("status").as_str())))
}

pub async fn start_run(pool: &PgPool, run_id: Uuid) -> anyhow::Result<()> {
    sqlx::query("UPDATE cron_job_runs SET started_at = now() WHERE id = $1")
        .bind(run_id)
        .execute(pool)
        .await?;
    Ok(())
}

pub async fn finish_run(
    pool: &PgPool,
    run_id: Uuid,
    status: RunStatus,
    error: Option<&str>,
    response_summary: Option<serde_json::Value>,
) -> anyhow::Result<()> {
    sqlx::query(
        "UPDATE cron_job_runs SET status = $1, error = $2, response_summary = $3, finished_at = now() \
         WHERE id = $4",
    )
    .bind(status.as_str())
    .bind(error)
    .bind(response_summary)
    .bind(run_id)
    .execute(pool)
    .await?;
    Ok(())
}

/// Records the outcome of `payload`'s firing, and — on failure with attempts remaining — also
/// schedules the next retry as a new `cron_job_runs` row (`attempt = payload.attempt + 1`,
/// `scheduled_for = now + backoff_delay(...)`, `status = "enqueued"`), which `claim_due_retries`
/// picks up once due. This is the retry-with-backoff called for in
/// `docs/features/02-workflow-engine.md`'s Increment 1 acceptance criteria — a failed firing no
/// longer just sits at `status: "failed"` forever if `max_attempts` allows another try. A
/// success, or a failure that has exhausted `max_attempts`, behaves exactly like `finish_run`
/// always did (no retry row).
pub async fn finish_run_with_retry(
    pool: &PgPool,
    payload: &CronJobDuePayload,
    status: RunStatus,
    error: Option<&str>,
    response_summary: Option<serde_json::Value>,
) -> anyhow::Result<()> {
    finish_run(pool, payload.run_id, status, error, response_summary).await?;

    if status != RunStatus::Failed || payload.attempt >= payload.max_attempts {
        return Ok(());
    }

    let next_attempt = payload.attempt + 1;
    let scheduled_for = Utc::now() + backoff_delay(payload.retry_backoff_seconds, payload.attempt);
    sqlx::query(
        "INSERT INTO cron_job_runs (id, tenant_id, job_id, status, attempt, scheduled_for) \
         VALUES ($1, $2, $3, $4, $5, $6)",
    )
    .bind(Uuid::new_v4())
    .bind(payload.tenant_id)
    .bind(payload.job_id)
    .bind(RunStatus::Enqueued.as_str())
    .bind(next_attempt)
    .bind(scheduled_for)
    .execute(pool)
    .await?;

    tracing::info!(
        job_id = %payload.job_id,
        run_id = %payload.run_id,
        next_attempt,
        %scheduled_for,
        "cron job failed, retry scheduled"
    );
    Ok(())
}
