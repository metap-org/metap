//! Claiming due firings (schedule-based and retries) and matching trigger-based ones
//! (`on_transition`/`on_record_event`), turning each into either an outbox `cron.job.due` event
//! or a `ClaimedDirectJob` for the caller to run in-process — the "how a firing actually gets
//! dispatched" half of this crate, complementing `super::job_crud`'s plain definition storage.

use chrono::{DateTime, Utc};
use metap_infra::{enqueue_outbox_event, OutboxEvent};
use sqlx::{PgPool, Row};
use uuid::Uuid;

use crate::model::{ClaimedDirectJob, CronJob, CronJobDuePayload, DispatchMode, RunStatus, ROUTING_KEY};
use crate::schedule::next_run_at;

use super::job_crud::job_from_row;

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

        dispatch_claimed(&mut tx, &job, run_id, 1, None, None, &mut result).await?;

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
/// schedule-based firing, called by `cron-scheduler`'s trigger-listener consumer instead of the
/// ticker. Same per-firing shape (a `cron_job_runs` row + outbox event or direct-dispatch entry)
/// so both trigger kinds share one execution/retry path downstream.
pub async fn dispatch_on_transition_matches(
    pool: &PgPool,
    tenant_id: Uuid,
    entity: &str,
    action: &str,
    record_id: Uuid,
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
    let result = fire_matched_jobs(&mut tx, rows, "on_transition", entity, action, Some(record_id)).await?;
    tx.commit().await?;
    Ok(result)
}

/// Fires every enabled `trigger_type = "on_record_event"` job whose `trigger_config` matches
/// `(tenant_id, entity, event)` (`event` is `"created"`/`"updated"`/`"deleted"`) —
/// `docs/roadmap/38-generic-record-event-triggers.md`, the `on_record_event` counterpart to
/// `dispatch_on_transition_matches` above, called by the same trigger-listener consumer for
/// `<entity>.record.{created,updated,deleted}` topics instead of `.workflow.transitioned`.
pub async fn dispatch_on_record_event_matches(
    pool: &PgPool,
    tenant_id: Uuid,
    entity: &str,
    event: &str,
    record_id: Uuid,
) -> anyhow::Result<ClaimResult> {
    let mut tx = pool.begin().await?;
    let rows = sqlx::query(
        "SELECT * FROM cron_jobs \
         WHERE tenant_id = $1 AND enabled AND trigger_type = 'on_record_event' \
         AND trigger_config ->> 'entity' = $2 AND trigger_config ->> 'event' = $3",
    )
    .bind(tenant_id)
    .bind(entity)
    .bind(event)
    .fetch_all(&mut *tx)
    .await?;
    let result = fire_matched_jobs(&mut tx, rows, "on_record_event", entity, event, Some(record_id)).await?;
    tx.commit().await?;
    Ok(result)
}

/// Shared by `dispatch_on_transition_matches`/`dispatch_on_record_event_matches` — turns a
/// batch of already-matched `cron_jobs` rows into `cron_job_runs` + dispatch entries, the one
/// place that per-firing bookkeeping (run row, `last_run_at`, `dispatch_claimed`, log line)
/// lives so the two trigger kinds can't drift on how a match actually gets fired. `trigger_kind`
/// is `"on_transition"`/`"on_record_event"` and `detail` is that trigger's matched
/// action/event — both just go into the log line, `tracing::info!`'s message has to be a
/// literal so they can't be interpolated into it directly.
async fn fire_matched_jobs(
    tx: &mut sqlx::Transaction<'_, sqlx::Postgres>,
    rows: Vec<sqlx::postgres::PgRow>,
    trigger_kind: &'static str,
    entity: &str,
    detail: &str,
    record_id: Option<Uuid>,
) -> anyhow::Result<ClaimResult> {
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
        .execute(&mut **tx)
        .await?;

        sqlx::query("UPDATE cron_jobs SET last_run_at = $1, updated_at = now() WHERE id = $2")
            .bind(now)
            .bind(job.id)
            .execute(&mut **tx)
            .await?;

        dispatch_claimed(tx, &job, run_id, 1, Some(entity), record_id, &mut result).await?;

        tracing::info!(
            job_id = %job.id, run_id = %run_id, name = job.name, entity, trigger_kind, detail,
            "cron job triggered"
        );
    }

    Ok(result)
}

/// A claimed retry (`started_at` set) that never reaches `finish_run`/`finish_run_with_retry`
/// (status stays `enqueued` forever) is treated as abandoned after this long and reclaimed —
/// found live (`AUDIT_2.md`): `started_at` is set the moment the retry is *claimed* (right after
/// its outbox event is enqueued below), not when the executor actually picks it up, so a
/// dedicated-DB tenant's outbox that never drains (wrong pool, RabbitMQ down long-term) left the
/// retry claimed but never executed, permanently excluded from `WHERE r.started_at IS NULL` —
/// silently lost, no timeout, nothing an admin could see. 5 minutes is generous relative to the
/// default 5s tick interval (`cron-scheduler`'s `CRON_TICK_MS`) — long enough that a healthy
/// outbox/executor round-trip never falsely reclaims a retry still legitimately in flight, short
/// enough that a genuinely stuck one recovers within one operator's coffee break, not silently
/// forever.
const RETRY_CLAIM_STALE_AFTER: chrono::Duration = chrono::Duration::seconds(300);

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
    // source and the audit trail here). `OR r.started_at < $4` reclaims a stale claim (see
    // `RETRY_CLAIM_STALE_AFTER`'s doc comment) — a row this old with `status` still `enqueued`
    // never reached `finish_run`/`finish_run_with_retry`, so treat it as abandoned rather than
    // lost forever. `j.enabled` matches `claim_due_jobs`'s same filter — a job disabled after a
    // failure scheduled its retry must not still fire it.
    let stale_before = now - RETRY_CLAIM_STALE_AFTER;
    let rows = sqlx::query(
        "SELECT r.id AS run_id, r.attempt, r.started_at AS run_started_at, j.* FROM cron_job_runs r \
         JOIN cron_jobs j ON j.id = r.job_id \
         WHERE r.attempt > 1 AND r.status = $1 AND (r.started_at IS NULL OR r.started_at < $4) \
         AND r.scheduled_for <= $2 AND j.enabled \
         ORDER BY r.scheduled_for LIMIT $3 FOR UPDATE OF r SKIP LOCKED",
    )
    .bind(RunStatus::Enqueued.as_str())
    .bind(now)
    .bind(batch_size)
    .bind(stale_before)
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
        let reclaimed = row.try_get::<Option<DateTime<Utc>>, _>("run_started_at")?.is_some();
        dispatch_claimed(&mut tx, &job, run_id, attempt, None, None, &mut result).await?;
        sqlx::query("UPDATE cron_job_runs SET started_at = now() WHERE id = $1")
            .bind(run_id)
            .execute(&mut *tx)
            .await?;
        if reclaimed {
            tracing::warn!(
                job_id = %job.id, run_id = %run_id, attempt,
                "cron job retry reclaimed (previous claim never finished — stale outbox or crashed executor)"
            );
        } else {
            tracing::info!(job_id = %job.id, run_id = %run_id, attempt, "cron job retry claimed");
        }
    }

    tx.commit().await?;
    Ok(result)
}

/// Shared by `claim_due_jobs`/`dispatch_on_transition_matches`/`claim_due_retries` — the one
/// place a claimed firing actually becomes either an outbox event (`DispatchMode::Outbox`) or
/// an entry in `ClaimResult::direct_jobs` for the caller to run immediately
/// (`DispatchMode::Direct`), so the three claim paths can never dispatch differently for the
/// same `dispatch_mode`.
/// `trigger_entity`/`trigger_record_id` are `None` for a `schedule` firing (`claim_due_jobs`) and
/// for a retry (`claim_due_retries` — the original firing's trigger context isn't persisted
/// anywhere `cron_job_runs` can rejoin later, so a retried email/webhook loses the "which record"
/// reference its first attempt had; not fixed here, same scope the plan called for), and `Some`
/// only for `fire_matched_jobs`'s `on_transition`/`on_record_event` callers, which have the
/// triggering event's entity/`recordId` on hand.
async fn dispatch_claimed(
    tx: &mut sqlx::Transaction<'_, sqlx::Postgres>,
    job: &CronJob,
    run_id: Uuid,
    attempt: i32,
    trigger_entity: Option<&str>,
    trigger_record_id: Option<Uuid>,
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
                trigger_record_id,
                trigger_entity: trigger_entity.map(str::to_string),
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
                trigger_record_id,
                trigger_entity: trigger_entity.map(str::to_string),
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
