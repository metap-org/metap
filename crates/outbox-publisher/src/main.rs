use std::env;
use std::time::Duration;

use metap_infra::{connect_db, load_config, EventBus, RabbitEventBus};
use sqlx::{Row, Transaction};
use uuid::Uuid;

#[derive(Debug)]
struct OutboxRow {
    id: Uuid,
    topic: String,
    payload: serde_json::Value,
}

#[tokio::main]
async fn main() -> anyhow::Result<()> {
    let config = load_config()?;

    let poll_ms: u64 = env::var("OUTBOX_POLL_MS")
        .ok()
        .and_then(|v| v.parse().ok())
        .unwrap_or(1000);
    let batch_size: i64 = env::var("OUTBOX_BATCH_SIZE")
        .ok()
        .and_then(|v| v.parse().ok())
        .unwrap_or(100);

    eprintln!("[outbox-publisher] connecting to postgres...");
    let pool = connect_db(config.outbox_database_url()).await?;

    eprintln!("[outbox-publisher] connecting to rabbitmq...");
    let bus = RabbitEventBus::connect(&config.rabbitmq_url).await?;

    eprintln!("[outbox-publisher] ready, polling every {poll_ms}ms, batch={batch_size}");

    let mut shutdown = Box::pin(shutdown_signal());

    loop {
        tokio::select! {
            biased;
            _ = &mut shutdown => {
                eprintln!("[outbox-publisher] shutdown signal received, exiting");
                break;
            }
            result = publish_pending(&pool, &bus, batch_size) => {
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
                eprintln!("[outbox-publisher] shutdown signal received, exiting");
                break;
            }
            _ = tokio::time::sleep(Duration::from_millis(poll_ms)) => {}
        }
    }

    bus.close().await.ok();
    pool.close().await;

    Ok(())
}

async fn shutdown_signal() {
    let ctrl_c = async {
        tokio::signal::ctrl_c().await.ok();
    };

    #[cfg(unix)]
    let terminate = async {
        tokio::signal::unix::signal(tokio::signal::unix::SignalKind::terminate())
            .expect("failed to install SIGTERM handler")
            .recv()
            .await;
    };

    #[cfg(not(unix))]
    let terminate = std::future::pending::<()>();

    tokio::select! {
        _ = ctrl_c => {}
        _ = terminate => {}
    }
}

/// Same contract as OutboxService.publishPending (packages/core/src/core/outbox/outbox-service.ts):
/// SELECT ... FOR UPDATE SKIP LOCKED held open for the whole publish-then-mark-done cycle, so
/// concurrent workers skip rows this transaction has locked instead of double-publishing them.
/// A per-row publish failure bumps `attempts`/`last_error` and leaves the row for the next
/// poll cycle rather than failing the whole batch — matching the Node implementation's
/// per-row try/catch inside the same transaction.
async fn publish_pending(
    pool: &sqlx::PgPool,
    bus: &impl EventBus,
    batch_size: i64,
) -> anyhow::Result<()> {
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
            Err(err) => mark_failed(&mut tx, row.id, &err.to_string()).await?,
        }
    }

    tx.commit().await?;
    Ok(())
}

async fn mark_published(tx: &mut Transaction<'_, sqlx::Postgres>, id: Uuid) -> anyhow::Result<()> {
    sqlx::query("UPDATE outbox_events SET published_at = now(), last_error = NULL WHERE id = $1")
        .bind(id)
        .execute(&mut **tx)
        .await?;
    Ok(())
}

async fn mark_failed(
    tx: &mut Transaction<'_, sqlx::Postgres>,
    id: Uuid,
    error: &str,
) -> anyhow::Result<()> {
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
