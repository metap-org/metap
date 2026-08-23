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

use metap_infra::EventBus;
use sqlx::{PgPool, Postgres, Row, Transaction};
use uuid::Uuid;

#[derive(Debug)]
struct OutboxRow {
    id: Uuid,
    topic: String,
    payload: serde_json::Value,
}

/// Runs the poll/publish/sleep loop until `shutdown` resolves.
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
pub async fn run(
    pool: &PgPool,
    bus: &impl EventBus,
    poll_ms: u64,
    batch_size: i64,
    shutdown: impl std::future::Future<Output = ()>,
) -> anyhow::Result<()> {
    let mut shutdown = std::pin::pin!(shutdown);

    loop {
        tokio::select! {
            biased;
            _ = &mut shutdown => {
                tracing::info!("shutdown signal received, exiting");
                return Ok(());
            }
            result = publish_pending(pool, bus, batch_size) => {
                // Matches runOutboxPublisherLoop's Node behavior: publishPending isn't
                // wrapped in try/catch there either, so an unhandled batch failure crashes
                // the process rather than retrying silently — a process manager is expected
                // to restart the worker. Same contract here: propagate and exit non-zero.
                result?;
            }
        }

        tokio::select! {
            biased;
            _ = &mut shutdown => {
                tracing::info!("shutdown signal received, exiting");
                return Ok(());
            }
            _ = tokio::time::sleep(Duration::from_millis(poll_ms)) => {}
        }
    }
}

/// Same contract as OutboxService.publishPending (packages/core/src/core/outbox/outbox-service.ts):
/// SELECT ... FOR UPDATE SKIP LOCKED held open for the whole publish-then-mark-done cycle, so
/// concurrent workers skip rows this transaction has locked instead of double-publishing them.
/// A per-row publish failure bumps `attempts`/`last_error` and leaves the row for the next
/// poll cycle rather than failing the whole batch — matching the Node implementation's
/// per-row try/catch inside the same transaction.
async fn publish_pending(pool: &PgPool, bus: &impl EventBus, batch_size: i64) -> anyhow::Result<()> {
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
            }
        }
    }

    tx.commit().await?;
    Ok(())
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
