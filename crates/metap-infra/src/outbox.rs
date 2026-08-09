//! Mirrors `OutboxService.enqueue` (`packages/core/src/core/outbox/outbox-service.ts`) —
//! just the write half (insert a row in the same transaction as the business write). The
//! drain/publish half lives in `crates/outbox-publisher/` (Migration Order step 1) and
//! `event_bus.rs`'s `EventBus`; unlike the TS `OutboxService` class, this isn't bundled
//! with either, since `enqueue` never actually touches an `EventBus` — it only needs
//! whatever executor (pool or open transaction) the caller is already writing through,
//! exactly like the TS version's `executor` parameter.

use serde_json::Value;
use sqlx::PgExecutor;
use uuid::Uuid;

#[derive(Debug, Clone)]
pub struct OutboxEvent {
    pub topic: String,
    pub aggregate_type: String,
    pub aggregate_id: Uuid,
    pub payload: Value,
}

pub async fn enqueue<'c, E>(executor: E, event: &OutboxEvent) -> anyhow::Result<()>
where
    E: PgExecutor<'c>,
{
    sqlx::query(
        "INSERT INTO outbox_events (topic, aggregate_type, aggregate_id, payload) \
         VALUES ($1, $2, $3, $4)",
    )
    .bind(&event.topic)
    .bind(&event.aggregate_type)
    .bind(event.aggregate_id)
    .bind(&event.payload)
    .execute(executor)
    .await?;
    Ok(())
}
