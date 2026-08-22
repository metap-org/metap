//! Postgres-backed CRUD for `cron_jobs`/`cron_job_runs`, plus `claim_due_jobs` — the ticker's
//! whole job (see that function's doc comment). Plain `&PgPool` functions, not a trait —
//! matches `metap_peripherals::role_assignment`'s style, not `PolicyStore`'s: there's no
//! pluggable-storage requirement here the way there was for policies (see
//! `docs/architectures/09-adr.md`).

use chrono::{DateTime, Utc};
use metap_infra::{enqueue_outbox_event, OutboxEvent};
use sqlx::{PgPool, Row};
use uuid::Uuid;

use crate::model::{
    ClaimedDirectJob, CronJob, CronJobDuePayload, CronJobRun, DispatchMode, RunStatus, TriggerType, ROUTING_KEY,
};
use crate::schedule::next_run_at;

fn job_from_row(row: &sqlx::postgres::PgRow) -> anyhow::Result<CronJob> {
    Ok(CronJob {
        id: row.try_get("id")?,
        tenant_id: row.try_get("tenant_id")?,
        name: row.try_get("name")?,
        enabled: row.try_get("enabled")?,
        trigger_type: row.try_get("trigger_type")?,
        trigger_config: row.try_get("trigger_config")?,
        cron_expr: row.try_get("cron_expr")?,
        timezone: row.try_get("timezone")?,
        target_type: row.try_get("target_type")?,
        target_config: row.try_get("target_config")?,
        dispatch_mode: row.try_get("dispatch_mode")?,
        max_attempts: row.try_get("max_attempts")?,
        retry_backoff_seconds: row.try_get("retry_backoff_seconds")?,
        next_run_at: row.try_get("next_run_at")?,
        last_run_at: row.try_get("last_run_at")?,
        created_at: row.try_get("created_at")?,
        updated_at: row.try_get("updated_at")?,
        created_by: row.try_get("created_by")?,
    })
}

fn run_from_row(row: &sqlx::postgres::PgRow) -> anyhow::Result<CronJobRun> {
    Ok(CronJobRun {
        id: row.try_get("id")?,
        tenant_id: row.try_get("tenant_id")?,
        job_id: row.try_get("job_id")?,
        status: row.try_get("status")?,
        attempt: row.try_get("attempt")?,
        scheduled_for: row.try_get("scheduled_for")?,
        started_at: row.try_get("started_at")?,
        finished_at: row.try_get("finished_at")?,
        error: row.try_get("error")?,
        response_summary: row.try_get("response_summary")?,
        created_at: row.try_get("created_at")?,
    })
}

/// `retry_backoff_seconds * 2^(attempt - 1)` — attempt 1 failing schedules attempt 2 after
/// exactly `retry_backoff_seconds`, attempt 2 failing schedules attempt 3 after
/// `2 * retry_backoff_seconds`, and so on. `attempt` is the attempt that just failed (1-based).
fn backoff_delay(retry_backoff_seconds: i32, attempt: i32) -> chrono::Duration {
    let exponent = (attempt - 1).max(0);
    let seconds = i64::from(retry_backoff_seconds).saturating_mul(1i64 << exponent.min(30));
    chrono::Duration::seconds(seconds)
}

pub async fn list_jobs(pool: &PgPool, tenant_id: Uuid) -> anyhow::Result<Vec<CronJob>> {
    let rows = sqlx::query("SELECT * FROM cron_jobs WHERE tenant_id = $1 ORDER BY created_at")
        .bind(tenant_id)
        .fetch_all(pool)
        .await?;
    rows.iter().map(job_from_row).collect()
}

pub async fn get_job(pool: &PgPool, tenant_id: Uuid, id: Uuid) -> anyhow::Result<Option<CronJob>> {
    let row = sqlx::query("SELECT * FROM cron_jobs WHERE tenant_id = $1 AND id = $2")
        .bind(tenant_id)
        .bind(id)
        .fetch_optional(pool)
        .await?;
    row.as_ref().map(job_from_row).transpose()
}

#[derive(Debug, Clone)]
pub struct NewCronJob {
    pub name: String,
    pub trigger_type: String,
    pub trigger_config: Option<serde_json::Value>,
    /// Required (and used to compute the first `next_run_at`) only when `trigger_type ==
    /// "schedule"` — `None` for `"on_transition"`, which has no schedule at all.
    pub cron_expr: Option<String>,
    pub timezone: String,
    pub target_type: String,
    pub target_config: serde_json::Value,
    pub dispatch_mode: String,
    pub max_attempts: i32,
    pub retry_backoff_seconds: i32,
    pub enabled: bool,
}

pub async fn create_job(
    pool: &PgPool,
    tenant_id: Uuid,
    input: NewCronJob,
    created_by: Option<Uuid>,
) -> anyhow::Result<CronJob> {
    let next_run = match (TriggerType::parse(&input.trigger_type), &input.cron_expr) {
        (Some(TriggerType::Schedule), Some(cron_expr)) => Some(next_run_at(cron_expr, &input.timezone, Utc::now())?),
        (Some(TriggerType::Schedule), None) => anyhow::bail!("`cronExpr` is required when triggerType is \"schedule\""),
        _ => None,
    };
    let row = sqlx::query(
        "INSERT INTO cron_jobs \
         (tenant_id, name, enabled, trigger_type, trigger_config, cron_expr, timezone, target_type, \
          target_config, dispatch_mode, max_attempts, retry_backoff_seconds, next_run_at, created_by) \
         VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14) RETURNING *",
    )
    .bind(tenant_id)
    .bind(&input.name)
    .bind(input.enabled)
    .bind(&input.trigger_type)
    .bind(&input.trigger_config)
    .bind(&input.cron_expr)
    .bind(&input.timezone)
    .bind(&input.target_type)
    .bind(&input.target_config)
    .bind(&input.dispatch_mode)
    .bind(input.max_attempts)
    .bind(input.retry_backoff_seconds)
    .bind(next_run)
    .bind(created_by)
    .fetch_one(pool)
    .await?;
    job_from_row(&row)
}

#[derive(Debug, Clone, Default)]
pub struct JobUpdate {
    pub name: Option<String>,
    pub trigger_type: Option<String>,
    pub trigger_config: Option<serde_json::Value>,
    pub cron_expr: Option<String>,
    pub timezone: Option<String>,
    pub target_type: Option<String>,
    pub target_config: Option<serde_json::Value>,
    pub dispatch_mode: Option<String>,
    pub max_attempts: Option<i32>,
    pub retry_backoff_seconds: Option<i32>,
    pub enabled: Option<bool>,
}

/// Returns `Ok(None)` if no job with `id` exists for `tenant_id` (not found, not an error —
/// callers turn that into a 404).
pub async fn update_job(
    pool: &PgPool,
    tenant_id: Uuid,
    id: Uuid,
    update: JobUpdate,
) -> anyhow::Result<Option<CronJob>> {
    let Some(existing) = get_job(pool, tenant_id, id).await? else {
        return Ok(None);
    };

    let name = update.name.unwrap_or(existing.name);
    let trigger_type = update.trigger_type.unwrap_or(existing.trigger_type);
    let trigger_config = update.trigger_config.or(existing.trigger_config);
    let cron_expr = update.cron_expr.or(existing.cron_expr);
    let timezone = update.timezone.unwrap_or(existing.timezone);
    let target_type = update.target_type.unwrap_or(existing.target_type);
    let target_config = update.target_config.unwrap_or(existing.target_config);
    let dispatch_mode = update.dispatch_mode.unwrap_or(existing.dispatch_mode);
    let max_attempts = update.max_attempts.unwrap_or(existing.max_attempts);
    let retry_backoff_seconds = update.retry_backoff_seconds.unwrap_or(existing.retry_backoff_seconds);
    let enabled = update.enabled.unwrap_or(existing.enabled);
    // Re-derive next_run_at whenever the schedule might have changed, so an edit takes effect
    // on its new schedule immediately instead of waiting out whatever next_run_at the old
    // schedule had already computed. Only meaningful for `trigger_type == "schedule"` —
    // `on_transition` jobs have no schedule, so `next_run_at` stays `None`.
    let next_run = match (TriggerType::parse(&trigger_type), &cron_expr) {
        (Some(TriggerType::Schedule), Some(cron_expr)) => Some(next_run_at(cron_expr, &timezone, Utc::now())?),
        (Some(TriggerType::Schedule), None) => anyhow::bail!("`cronExpr` is required when triggerType is \"schedule\""),
        _ => None,
    };

    let row = sqlx::query(
        "UPDATE cron_jobs SET name = $1, trigger_type = $2, trigger_config = $3, cron_expr = $4, \
         timezone = $5, target_type = $6, target_config = $7, dispatch_mode = $8, max_attempts = $9, \
         retry_backoff_seconds = $10, enabled = $11, next_run_at = $12, updated_at = now() \
         WHERE tenant_id = $13 AND id = $14 RETURNING *",
    )
    .bind(&name)
    .bind(&trigger_type)
    .bind(&trigger_config)
    .bind(&cron_expr)
    .bind(&timezone)
    .bind(&target_type)
    .bind(&target_config)
    .bind(&dispatch_mode)
    .bind(max_attempts)
    .bind(retry_backoff_seconds)
    .bind(enabled)
    .bind(next_run)
    .bind(tenant_id)
    .bind(id)
    .fetch_one(pool)
    .await?;
    Ok(Some(job_from_row(&row)?))
}

pub async fn delete_job(pool: &PgPool, tenant_id: Uuid, id: Uuid) -> anyhow::Result<()> {
    sqlx::query("DELETE FROM cron_jobs WHERE tenant_id = $1 AND id = $2")
        .bind(tenant_id)
        .bind(id)
        .execute(pool)
        .await?;
    Ok(())
}

pub async fn list_job_runs(
    pool: &PgPool,
    tenant_id: Uuid,
    job_id: Uuid,
    limit: i64,
) -> anyhow::Result<Vec<CronJobRun>> {
    let rows = sqlx::query(
        "SELECT * FROM cron_job_runs WHERE tenant_id = $1 AND job_id = $2 \
         ORDER BY created_at DESC LIMIT $3",
    )
    .bind(tenant_id)
    .bind(job_id)
    .bind(limit)
    .fetch_all(pool)
    .await?;
    rows.iter().map(run_from_row).collect()
}

#[derive(Debug, Default)]
pub struct ClaimResult {
    /// Total jobs claimed this batch, `Outbox` and `Direct` combined — for the ticker's log
    /// line.
    pub claimed: usize,
    /// The `Direct`-mode subset, which the ticker must execute itself right now — `claim_due_jobs`
    /// only writes an outbox event for `Outbox`-mode jobs, so these don't go through
    /// `outbox-publisher`/RabbitMQ at all.
    pub direct_jobs: Vec<ClaimedDirectJob>,
}

/// Claims every job due at or before `now` (`SELECT ... FOR UPDATE SKIP LOCKED` — same
/// concurrency-safety pattern `outbox-publisher::publish_pending` uses, so multiple
/// `cron-scheduler` replicas never double-fire the same job), advances each one's
/// `next_run_at` to its next occurrence, and inserts a `cron_job_runs` row (`status =
/// "enqueued"`) — all in one transaction per claimed batch, so a crash mid-batch can't
/// duplicate a firing. What happens next depends on the job's `DispatchMode`: `Outbox` jobs
/// get a `cron.job.due` outbox event written in the same transaction (so a crash between
/// claim and outbox-write can't lose one either); `Direct` jobs get no outbox event at all —
/// they come back in `ClaimResult::direct_jobs` for the caller to execute immediately, with no
/// durability guarantee beyond the `cron_job_runs` row already written.
pub async fn claim_due_jobs(pool: &PgPool, now: DateTime<Utc>, batch_size: i64) -> anyhow::Result<ClaimResult> {
    let mut tx = pool.begin().await?;

    // `next_run_at IS NULL` for every `trigger_type = "on_transition"` job, so `next_run_at <=
    // $1` naturally excludes them here (NULL comparisons are never true in SQL) without an
    // explicit `trigger_type = 'schedule'` filter.
    let rows = sqlx::query(
        "SELECT * FROM cron_jobs WHERE enabled AND next_run_at <= $1 \
         ORDER BY next_run_at LIMIT $2 FOR UPDATE SKIP LOCKED",
    )
    .bind(now)
    .bind(batch_size)
    .fetch_all(&mut *tx)
    .await?;

    let mut result = ClaimResult {
        claimed: rows.len(),
        direct_jobs: Vec::new(),
    };

    for row in &rows {
        let job = job_from_row(row)?;
        let Some(cron_expr) = &job.cron_expr else {
            // Can't happen for a row this query matched (see the NULL-exclusion note above) —
            // guarded rather than unwrapped so a future schema/query change fails loudly here
            // instead of panicking.
            tracing::error!(job_id = %job.id, "disabling cron job: schedule job with no cron_expr");
            sqlx::query("UPDATE cron_jobs SET enabled = false, updated_at = now() WHERE id = $1")
                .bind(job.id)
                .execute(&mut *tx)
                .await?;
            continue;
        };
        let next = match next_run_at(cron_expr, &job.timezone, now) {
            Ok(next) => next,
            Err(err) => {
                // A job whose schedule became uncomputable (edited around validation, or a
                // one-shot expression with no future occurrence) shouldn't wedge the whole
                // batch on every future tick — disable it and move on rather than reclaiming
                // it forever.
                tracing::error!(job_id = %job.id, error = %err, "disabling cron job: cannot compute next run");
                sqlx::query("UPDATE cron_jobs SET enabled = false, updated_at = now() WHERE id = $1")
                    .bind(job.id)
                    .execute(&mut *tx)
                    .await?;
                continue;
            }
        };

        sqlx::query("UPDATE cron_jobs SET next_run_at = $1, last_run_at = $2, updated_at = now() WHERE id = $3")
            .bind(next)
            .bind(now)
            .bind(job.id)
            .execute(&mut *tx)
            .await?;

        let run_id = Uuid::new_v4();
        sqlx::query(
            "INSERT INTO cron_job_runs (id, tenant_id, job_id, status, scheduled_for) \
             VALUES ($1, $2, $3, $4, $5)",
        )
        .bind(run_id)
        .bind(job.tenant_id)
        .bind(job.id)
        .bind(RunStatus::Enqueued.as_str())
        .bind(job.next_run_at) // the occurrence that was actually due, not the newly-advanced one
        .execute(&mut *tx)
        .await?;

        dispatch_claimed(&mut tx, &job, run_id, 1, &mut result).await?;

        tracing::info!(
            job_id = %job.id,
            run_id = %run_id,
            name = job.name,
            dispatch_mode = job.dispatch_mode,
            "cron job claimed"
        );
    }

    tx.commit().await?;
    Ok(result)
}

/// Fires every enabled `trigger_type = "on_transition"` job whose `trigger_config` matches
/// `(tenant_id, entity, action)` — the `on_transition` counterpart to `claim_due_jobs`'s
/// schedule-based firing, called by `cron-scheduler`'s `#.workflow.transitioned` consumer
/// instead of the ticker. Same per-firing shape (a `cron_job_runs` row + outbox event or
/// direct-dispatch entry) so both trigger kinds share one execution/retry path downstream.
pub async fn dispatch_on_transition_matches(
    pool: &PgPool,
    tenant_id: Uuid,
    entity: &str,
    action: &str,
) -> anyhow::Result<ClaimResult> {
    let mut tx = pool.begin().await?;

    let rows = sqlx::query(
        "SELECT * FROM cron_jobs \
         WHERE tenant_id = $1 AND enabled AND trigger_type = 'on_transition' \
         AND trigger_config ->> 'entity' = $2 AND trigger_config ->> 'action' = $3",
    )
    .bind(tenant_id)
    .bind(entity)
    .bind(action)
    .fetch_all(&mut *tx)
    .await?;

    let mut result = ClaimResult {
        claimed: rows.len(),
        direct_jobs: Vec::new(),
    };

    let now = Utc::now();
    for row in &rows {
        let job = job_from_row(row)?;
        let run_id = Uuid::new_v4();
        sqlx::query(
            "INSERT INTO cron_job_runs (id, tenant_id, job_id, status, scheduled_for) \
             VALUES ($1, $2, $3, $4, $5)",
        )
        .bind(run_id)
        .bind(job.tenant_id)
        .bind(job.id)
        .bind(RunStatus::Enqueued.as_str())
        .bind(now)
        .execute(&mut *tx)
        .await?;

        sqlx::query("UPDATE cron_jobs SET last_run_at = $1, updated_at = now() WHERE id = $2")
            .bind(now)
            .bind(job.id)
            .execute(&mut *tx)
            .await?;

        dispatch_claimed(&mut tx, &job, run_id, 1, &mut result).await?;

        tracing::info!(
            job_id = %job.id,
            run_id = %run_id,
            name = job.name,
            entity,
            action,
            "cron job triggered on transition"
        );
    }

    tx.commit().await?;
    Ok(result)
}

/// Claims every retry due at or before `now` — a `cron_job_runs` row with `attempt > 1` that a
/// prior failed attempt scheduled (see `finish_run_with_retry`). Same `FOR UPDATE SKIP LOCKED`
/// safety as `claim_due_jobs`, joined to the owning `cron_jobs` row for the
/// `target_type`/`target_config`/`dispatch_mode` needed to actually redispatch it.
pub async fn claim_due_retries(pool: &PgPool, now: DateTime<Utc>, batch_size: i64) -> anyhow::Result<ClaimResult> {
    let mut tx = pool.begin().await?;

    // `r.started_at IS NULL` is the claim marker: set in the same transaction right after
    // dispatch below, so a retry row can never be picked up twice by two ticks (the equivalent
    // of `claim_due_jobs` advancing `next_run_at` — that one moves the *source* row out of its
    // own claim window, this one has to mark itself since `cron_job_runs` is both the claim
    // source and the audit trail here). `j.enabled` matches `claim_due_jobs`'s same filter — a
    // job disabled after a failure scheduled its retry must not still fire it.
    let rows = sqlx::query(
        "SELECT r.id AS run_id, r.attempt, j.* FROM cron_job_runs r \
         JOIN cron_jobs j ON j.id = r.job_id \
         WHERE r.attempt > 1 AND r.status = $1 AND r.started_at IS NULL AND r.scheduled_for <= $2 \
         AND j.enabled \
         ORDER BY r.scheduled_for LIMIT $3 FOR UPDATE OF r SKIP LOCKED",
    )
    .bind(RunStatus::Enqueued.as_str())
    .bind(now)
    .bind(batch_size)
    .fetch_all(&mut *tx)
    .await?;

    let mut result = ClaimResult {
        claimed: rows.len(),
        direct_jobs: Vec::new(),
    };

    for row in &rows {
        let job = job_from_row(row)?;
        let run_id: Uuid = row.try_get("run_id")?;
        let attempt: i32 = row.try_get("attempt")?;
        dispatch_claimed(&mut tx, &job, run_id, attempt, &mut result).await?;
        sqlx::query("UPDATE cron_job_runs SET started_at = now() WHERE id = $1")
            .bind(run_id)
            .execute(&mut *tx)
            .await?;
        tracing::info!(job_id = %job.id, run_id = %run_id, attempt, "cron job retry claimed");
    }

    tx.commit().await?;
    Ok(result)
}

/// Shared by `claim_due_jobs`/`dispatch_on_transition_matches`/`claim_due_retries` — the one
/// place a claimed firing actually becomes either an outbox event (`DispatchMode::Outbox`) or
/// an entry in `ClaimResult::direct_jobs` for the caller to run immediately
/// (`DispatchMode::Direct`), so the three claim paths can never dispatch differently for the
/// same `dispatch_mode`.
async fn dispatch_claimed(
    tx: &mut sqlx::Transaction<'_, sqlx::Postgres>,
    job: &CronJob,
    run_id: Uuid,
    attempt: i32,
    result: &mut ClaimResult,
) -> anyhow::Result<()> {
    match DispatchMode::parse(&job.dispatch_mode) {
        Some(DispatchMode::Direct) => {
            result.direct_jobs.push(ClaimedDirectJob {
                run_id,
                job_id: job.id,
                tenant_id: job.tenant_id,
                target_type: job.target_type.clone(),
                target_config: job.target_config.clone(),
                attempt,
                max_attempts: job.max_attempts,
                retry_backoff_seconds: job.retry_backoff_seconds,
                dispatch_mode: job.dispatch_mode.clone(),
            });
        }
        // Unknown dispatch_mode (shouldn't happen — validated at write time) falls back to the
        // reliable path rather than silently dropping the firing.
        Some(DispatchMode::Outbox) | None => {
            let payload = CronJobDuePayload {
                run_id,
                job_id: job.id,
                tenant_id: job.tenant_id,
                target_type: job.target_type.clone(),
                target_config: job.target_config.clone(),
                attempt,
                max_attempts: job.max_attempts,
                retry_backoff_seconds: job.retry_backoff_seconds,
                dispatch_mode: job.dispatch_mode.clone(),
            };
            enqueue_outbox_event(
                &mut **tx,
                &OutboxEvent {
                    topic: ROUTING_KEY.to_string(),
                    aggregate_type: "cron_job".to_string(),
                    aggregate_id: job.id,
                    payload: serde_json::to_value(&payload)?,
                },
            )
            .await?;
        }
    }
    Ok(())
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
