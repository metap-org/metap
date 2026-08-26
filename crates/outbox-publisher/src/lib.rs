//! The outbox pattern's read side — `metap-infra::outbox::enqueue` (the write side) writes a
//! row into `outbox_events` inside the same transaction as a business write; this loop drains
//! that table and publishes each row to `EventBus`, so RabbitMQ downtime can't lose an event
//! (`docs/architectures/09-adr.md`).
//!
//! Binary+lib, same shape as `metap-notification-worker`: this crate's own standalone binary
//! (`src/main.rs`) is the normal deployment shape (`pnpm worker:outbox:rs`), but a host process
//! can also run this loop inline against its own already-resolved pool instead of spawning a
//! separate process — see `apps/jira-server`'s `OUTBOX_WORKER_INLINE` flag. Both call this same
//! `run()`, so the two deployment shapes can't drift apart.

use std::time::Duration;

use metap_infra::{backoff_delay, sleep_or_shutdown, EventBus};
use sqlx::{PgPool, Postgres, Row, Transaction};
use uuid::Uuid;

#[derive(Debug)]
struct OutboxRow {
    id: Uuid,
    topic: String,
    payload: serde_json::Value,
}

/// Runs the poll/publish/sleep loop until `shutdown` resolves, reconnecting the event bus with
/// backoff (`metap_infra::backoff_delay`, the same schedule `run_resilient_consumer` uses) after
/// any publish failure — before this, `run` took an already-connected `&impl EventBus` with no
/// way to recover: a RabbitMQ restart/blip meant `bus.publish` failed silently forever afterward
/// (every pending event correctly re-queued via `mark_failed`, but never actually published
/// again) until an operator noticed and restarted the process by hand. Mirrors
/// `notification-worker`/`cron-scheduler`'s consumers (Phase 37), which had exactly this same gap
/// on their side before `run_resilient_consumer` existed — this crate is the publish-side
/// counterpart, since `run_resilient_consumer` itself only covers `subscribe`.
///
/// **`pool` must be the same database `metap_infra::outbox::enqueue` actually wrote the rows
/// into for whatever tenant this instance is meant to drain** — outbox rows live wherever the
/// business write that created them landed (`Router::begin`), not necessarily a fixed
/// `DATABASE_URL`. For a `Schema`-strategy tenant that's the shared platform database (the
/// common case, what the standalone binary's `config.outbox_database_url()` already points at);
/// for a `DedicatedDb` tenant (e.g. `apps/jira-server`'s tenant) it's that tenant's own
/// dedicated pool — pointing this at the platform's pool instead would silently drain nothing,
/// forever, for that tenant's events (found live: `apps/jira-server`'s workflow-transition
/// outbox rows sat unpublished with no worker draining them at all before this existed).
pub async fn run<B, F, Fut>(
    pool: &PgPool,
    connect: F,
    poll_ms: u64,
    batch_size: i64,
    shutdown: impl std::future::Future<Output = ()>,
) -> anyhow::Result<()>
where
    B: EventBus,
    F: Fn() -> Fut,
    Fut: std::future::Future<Output = anyhow::Result<B>>,
{
    let mut shutdown = std::pin::pin!(shutdown);
    let mut attempt: u32 = 0;

    let mut bus = 'connect: loop {
        match connect().await {
            Ok(bus) => break 'connect bus,
            Err(err) => {
                tracing::warn!(error = %err, attempt, "failed to connect to event bus, retrying");
                if !sleep_or_shutdown(backoff_delay(attempt), &mut shutdown).await {
                    return Ok(());
                }
                attempt += 1;
            }
        }
    };

    loop {
        let batch_result = tokio::select! {
            biased;
            _ = &mut shutdown => {
                tracing::info!("shutdown signal received, exiting");
                bus.close().await.ok();
                return Ok(());
            }
            result = publish_pending(pool, &bus, batch_size) => result,
        };

        match batch_result {
            // Matches runOutboxPublisherLoop's Node behavior: a DB-level failure (the
            // transaction/query itself, not an individual row's publish — see
            // `publish_pending`'s doc comment) isn't retried here either, it crashes the process
            // for a process manager to restart, same contract as before this fix.
            Err(err) => return Err(err),
            // At least one row in the batch failed to publish — `RabbitEventBus::publish`'s only
            // realistic failure mode is a broken channel/connection, so treat any row failure as
            // "the bus itself needs reconnecting" rather than waiting for the next poll cycle to
            // fail the exact same way forever.
            Ok(false) => {
                tracing::warn!("publish failure detected, reconnecting to event bus");
                bus.close().await.ok();
                attempt = 0;
                bus = 'reconnect: loop {
                    if !sleep_or_shutdown(backoff_delay(attempt), &mut shutdown).await {
                        return Ok(());
                    }
                    match connect().await {
                        Ok(bus) => break 'reconnect bus,
                        Err(err) => {
                            tracing::warn!(error = %err, attempt, "failed to reconnect to event bus, retrying");
                            attempt += 1;
                        }
                    }
                };
            }
            Ok(true) => {}
        }

        if !sleep_or_shutdown(Duration::from_millis(poll_ms), &mut shutdown).await {
            bus.close().await.ok();
            return Ok(());
        }
    }
}

/// Same contract as OutboxService.publishPending (packages/core/src/core/outbox/outbox-service.ts):
/// SELECT ... FOR UPDATE SKIP LOCKED held open for the whole publish-then-mark-done cycle, so
/// concurrent workers skip rows this transaction has locked instead of double-publishing them.
/// A per-row publish failure bumps `attempts`/`last_error` and leaves the row for the next
/// poll cycle rather than failing the whole batch — matching the Node implementation's
/// per-row try/catch inside the same transaction. Returns `Ok(false)` (distinct from an `Err`,
/// which is reserved for a DB-level failure — the query/transaction itself) when at least one
/// row failed to publish, so `run` knows to reconnect the bus before the next poll.
async fn publish_pending(pool: &PgPool, bus: &impl EventBus, batch_size: i64) -> anyhow::Result<bool> {
    let mut tx = pool.begin().await?;

    let rows = sqlx::query(
        "SELECT id, topic, payload FROM outbox_events \
         WHERE published_at IS NULL \
         ORDER BY created_at \
         LIMIT $1 \
         FOR UPDATE SKIP LOCKED",
    )
    .bind(batch_size)
    .fetch_all(&mut *tx)
    .await?
    .into_iter()
    .map(|r| OutboxRow {
        id: r.get("id"),
        topic: r.get("topic"),
        payload: r.get("payload"),
    })
    .collect::<Vec<_>>();

    let mut all_published = true;
    for row in rows {
        match bus.publish(&row.topic, &row.payload).await {
            Ok(()) => mark_published(&mut tx, row.id).await?,
            Err(err) => {
                tracing::warn!(
                    outbox_id = %row.id,
                    topic = row.topic,
                    error = %err,
                    "failed to publish outbox event, will retry next poll"
                );
                mark_failed(&mut tx, row.id, &err.to_string()).await?;
                all_published = false;
            }
        }
    }

    tx.commit().await?;
    Ok(all_published)
}

async fn mark_published(tx: &mut Transaction<'_, Postgres>, id: Uuid) -> anyhow::Result<()> {
    sqlx::query("UPDATE outbox_events SET published_at = now(), last_error = NULL WHERE id = $1")
        .bind(id)
        .execute(&mut **tx)
        .await?;
    Ok(())
}

async fn mark_failed(tx: &mut Transaction<'_, Postgres>, id: Uuid, error: &str) -> anyhow::Result<()> {
    sqlx::query(
        "UPDATE outbox_events SET attempts = attempts + 1, last_error = $1 \
         WHERE id = $2 AND published_at IS NULL",
    )
    .bind(error)
    .bind(id)
    .execute(&mut **tx)
    .await?;
    Ok(())
}
