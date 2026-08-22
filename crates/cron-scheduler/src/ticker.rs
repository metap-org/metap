//! The polling half: claims due jobs (see `metap_cron::claim_due_jobs`'s doc comment) and
//! either writes their `cron.job.due` outbox event (`DispatchMode::Outbox`, the default) or —
//! for `DispatchMode::Direct` jobs — executes them immediately, in-process, in the same tick
//! that claimed them (see `metap_cron::DispatchMode`'s doc comment for the tradeoff). Mirrors
//! `outbox-publisher::main`'s loop shape (biased `tokio::select!` against shutdown, then a
//! sleep, both shutdown-interruptible) so the two ops binaries read the same way.

use std::time::Duration;

use metap_cron::{ClaimedDirectJob, CronJobDuePayload};
use sqlx::PgPool;

use crate::executor::{execute, ExecutorConfig};

#[derive(Debug, Clone, Copy)]
pub struct TickerConfig {
    pub interval: Duration,
    pub batch_size: i64,
}

pub async fn run_ticker(
    pool: &PgPool,
    http: &reqwest::Client,
    executor_config: &ExecutorConfig,
    config: TickerConfig,
    shutdown: impl std::future::Future<Output = ()>,
) -> anyhow::Result<()> {
    let mut shutdown = std::pin::pin!(shutdown);

    loop {
        tokio::select! {
            biased;
            _ = &mut shutdown => {
                tracing::info!("shutdown signal received, exiting ticker");
                return Ok(());
            }
            result = tick(pool, http, executor_config, config.batch_size) => {
                result?;
            }
        }

        tokio::select! {
            biased;
            _ = &mut shutdown => {
                tracing::info!("shutdown signal received, exiting ticker");
                return Ok(());
            }
            _ = tokio::time::sleep(config.interval) => {}
        }
    }
}

async fn tick(
    pool: &PgPool,
    http: &reqwest::Client,
    executor_config: &ExecutorConfig,
    batch_size: i64,
) -> anyhow::Result<()> {
    let due = metap_cron::claim_due_jobs(pool, chrono::Utc::now(), batch_size).await?;
    if due.claimed > 0 {
        tracing::info!(
            claimed = due.claimed,
            direct = due.direct_jobs.len(),
            "cron ticker claimed due jobs"
        );
    }
    run_direct_jobs(pool, http, executor_config, due.direct_jobs).await;

    // Retries scheduled by a prior failed attempt (`finish_run_with_retry`) — same claim/dispatch
    // shape as `claim_due_jobs`, just sourced from `cron_job_runs` instead of `cron_jobs`.
    let retries = metap_cron::claim_due_retries(pool, chrono::Utc::now(), batch_size).await?;
    if retries.claimed > 0 {
        tracing::info!(
            claimed = retries.claimed,
            direct = retries.direct_jobs.len(),
            "cron ticker claimed due retries"
        );
    }
    run_direct_jobs(pool, http, executor_config, retries.direct_jobs).await;

    Ok(())
}

// `Direct`-mode jobs never touch the outbox/RabbitMQ — run them right here, sequentially. A
// slow direct job delays the next tick's claim, which is the fire-and-forget tradeoff this
// dispatch mode signs up for (see `metap_cron::DispatchMode`'s doc comment); a job that can't
// tolerate that delay should use `DispatchMode::Outbox` instead.
async fn run_direct_jobs(
    pool: &PgPool,
    http: &reqwest::Client,
    executor_config: &ExecutorConfig,
    direct_jobs: Vec<ClaimedDirectJob>,
) {
    for direct_job in direct_jobs {
        let payload = CronJobDuePayload {
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
}
