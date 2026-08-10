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
    ClaimedDirectJob, CronJob, CronJobDuePayload, CronJobRun, DispatchMode, RunStatus, ROUTING_KEY,
};
use crate::schedule::next_run_at;

fn job_from_row(row: &sqlx::postgres::PgRow) -> anyhow::Result<CronJob> {
    Ok(CronJob {
        id: row.try_get("id")?,
        tenant_id: row.try_get("tenant_id")?,
        name: row.try_get("name")?,
        enabled: row.try_get("enabled")?,
        cron_expr: row.try_get("cron_expr")?,
        timezone: row.try_get("timezone")?,
        target_type: row.try_get("target_type")?,
        target_config: row.try_get("target_config")?,
        dispatch_mode: row.try_get("dispatch_mode")?,
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
        scheduled_for: row.try_get("scheduled_for")?,
        started_at: row.try_get("started_at")?,
        finished_at: row.try_get("finished_at")?,
        error: row.try_get("error")?,
        response_summary: row.try_get("response_summary")?,
        created_at: row.try_get("created_at")?,
    })
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
    pub cron_expr: String,
    pub timezone: String,
    pub target_type: String,
    pub target_config: serde_json::Value,
    pub dispatch_mode: String,
    pub enabled: bool,
}

pub async fn create_job(
    pool: &PgPool,
    tenant_id: Uuid,
    input: NewCronJob,
    created_by: Option<Uuid>,
) -> anyhow::Result<CronJob> {
    let first_run = next_run_at(&input.cron_expr, &input.timezone, Utc::now())?;
    let row = sqlx::query(
        "INSERT INTO cron_jobs \
         (tenant_id, name, enabled, cron_expr, timezone, target_type, target_config, dispatch_mode, next_run_at, created_by) \
         VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) RETURNING *",
    )
    .bind(tenant_id)
    .bind(&input.name)
    .bind(input.enabled)
    .bind(&input.cron_expr)
    .bind(&input.timezone)
    .bind(&input.target_type)
    .bind(&input.target_config)
    .bind(&input.dispatch_mode)
    .bind(first_run)
    .bind(created_by)
    .fetch_one(pool)
    .await?;
    job_from_row(&row)
}

#[derive(Debug, Clone, Default)]
pub struct JobUpdate {
    pub name: Option<String>,
    pub cron_expr: Option<String>,
    pub timezone: Option<String>,
    pub target_type: Option<String>,
    pub target_config: Option<serde_json::Value>,
    pub dispatch_mode: Option<String>,
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
    let Some(existing) = get_job(pool, tenant_id, id).await? else { return Ok(None) };

    let name = update.name.unwrap_or(existing.name);
    let cron_expr = update.cron_expr.unwrap_or(existing.cron_expr);
    let timezone = update.timezone.unwrap_or(existing.timezone);
    let target_type = update.target_type.unwrap_or(existing.target_type);
    let target_config = update.target_config.unwrap_or(existing.target_config);
    let dispatch_mode = update.dispatch_mode.unwrap_or(existing.dispatch_mode);
    let enabled = update.enabled.unwrap_or(existing.enabled);
    // Re-derive next_run_at whenever the schedule might have changed, so an edit takes effect
    // on its new schedule immediately instead of waiting out whatever next_run_at the old
    // schedule had already computed.
    let next_run = next_run_at(&cron_expr, &timezone, Utc::now())?;

    let row = sqlx::query(
        "UPDATE cron_jobs SET name = $1, cron_expr = $2, timezone = $3, target_type = $4, \
         target_config = $5, dispatch_mode = $6, enabled = $7, next_run_at = $8, updated_at = now() \
         WHERE tenant_id = $9 AND id = $10 RETURNING *",
    )
    .bind(&name)
    .bind(&cron_expr)
    .bind(&timezone)
    .bind(&target_type)
    .bind(&target_config)
    .bind(&dispatch_mode)
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
pub async fn claim_due_jobs(
    pool: &PgPool,
    now: DateTime<Utc>,
    batch_size: i64,
) -> anyhow::Result<ClaimResult> {
    let mut tx = pool.begin().await?;

    let rows = sqlx::query(
        "SELECT * FROM cron_jobs WHERE enabled AND next_run_at <= $1 \
         ORDER BY next_run_at LIMIT $2 FOR UPDATE SKIP LOCKED",
    )
    .bind(now)
    .bind(batch_size)
    .fetch_all(&mut *tx)
    .await?;

    let mut result = ClaimResult { claimed: rows.len(), direct_jobs: Vec::new() };

    for row in &rows {
        let job = job_from_row(row)?;
        let next = match next_run_at(&job.cron_expr, &job.timezone, now) {
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

        sqlx::query(
            "UPDATE cron_jobs SET next_run_at = $1, last_run_at = $2, updated_at = now() WHERE id = $3",
        )
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

        match DispatchMode::parse(&job.dispatch_mode) {
            Some(DispatchMode::Direct) => {
                result.direct_jobs.push(ClaimedDirectJob {
                    run_id,
                    job_id: job.id,
                    tenant_id: job.tenant_id,
                    target_type: job.target_type.clone(),
                    target_config: job.target_config.clone(),
                });
            }
            // Unknown dispatch_mode (shouldn't happen — validated at write time) falls back
            // to the reliable path rather than silently dropping the firing.
            Some(DispatchMode::Outbox) | None => {
                let payload = CronJobDuePayload {
                    run_id,
                    job_id: job.id,
                    tenant_id: job.tenant_id,
                    target_type: job.target_type.clone(),
                    target_config: job.target_config.clone(),
                };
                enqueue_outbox_event(
                    &mut *tx,
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
