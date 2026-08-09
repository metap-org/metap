//! The polling half: claims due jobs (see `metap_cron::claim_due_jobs`'s doc comment) and
//! either writes their `cron.job.due` outbox event (`DispatchMode::Outbox`, the default) or —
//! for `DispatchMode::Direct` jobs — executes them immediately, in-process, in the same tick
//! that claimed them (see `metap_cron::DispatchMode`'s doc comment for the tradeoff). Mirrors
//! `outbox-publisher::main`'s loop shape (biased `tokio::select!` against shutdown, then a
//! sleep, both shutdown-interruptible) so the two ops binaries read the same way.

use std::time::Duration;

use metap_cron::CronJobDuePayload;
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
    let result = metap_cron::claim_due_jobs(pool, chrono::Utc::now(), batch_size).await?;
    if result.claimed > 0 {
        tracing::info!(
            claimed = result.claimed,
            direct = result.direct_jobs.len(),
            "cron ticker claimed due jobs"
        );
    }

    // `Direct`-mode jobs never touch the outbox/RabbitMQ — run them right here, sequentially.
    // A slow direct job delays the next tick's claim, which is the fire-and-forget tradeoff
    // this dispatch mode signs up for (see `metap_cron::DispatchMode`'s doc comment); a job
    // that can't tolerate that delay should use `DispatchMode::Outbox` instead.
    for direct_job in result.direct_jobs {
        let payload = CronJobDuePayload {
            run_id: direct_job.run_id,
            job_id: direct_job.job_id,
            tenant_id: direct_job.tenant_id,
            target_type: direct_job.target_type,
            target_config: direct_job.target_config,
        };
        execute(pool, http, executor_config, &payload).await;
    }

    Ok(())
}
