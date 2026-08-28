//! Plain CRUD over `cron_jobs`/`cron_job_runs` — create/read/update/delete a job definition and
//! list its run history. Claiming/firing/dispatching a due job lives in `super::dispatch`
//! instead; this file is purely the admin-facing definition store.

use chrono::Utc;
use sqlx::{PgPool, Row};
use uuid::Uuid;

use crate::model::{CronJob, CronJobRun, TriggerType};
use crate::schedule::next_run_at;

pub(crate) fn job_from_row(row: &sqlx::postgres::PgRow) -> anyhow::Result<CronJob> {
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
