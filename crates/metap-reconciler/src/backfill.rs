//! §5.7 — checkpointed batch backfill, shared by two callers: `executor` (populating a newly
//! promoted `storage: column` field from `data ->> source_field`) and `migration`
//! (transforming `data` in place for a `widen_type` op). Four points from the design doc, all
//! present here: keyset pagination (`id > cursor`, no `OFFSET`); the checkpoint is saved in the
//! *same transaction* as the batch update (atomic — a crash between them is impossible, so
//! resume is always exactly where the last committed batch left off); a small sleep between
//! batches so this never starves autovacuum/replication; cancellable is left to the caller (a
//! normal `Result`, so "don't call this again" is enough to stop).

use sqlx::PgPool;
use uuid::Uuid;

use crate::sqlfmt::{quote_ident, quote_literal, quote_qualified_ident};

const BATCH_SIZE: i64 = 5000;
const THROTTLE: std::time::Duration = std::time::Duration::from_millis(20);

#[allow(clippy::too_many_arguments)]
pub async fn run_heavy_backfill(
    pool: &PgPool,
    tenant_id: Uuid,
    entity_name: &str,
    table: &str,
    op_id: &str,
    column: &str,
    source_field: &str,
    sql_type: &str,
) -> anyhow::Result<()> {
    let quoted_col = quote_ident(column);
    let field_literal = quote_literal(source_field);
    let set_clause = format!("{quoted_col} = (t.data ->> {field_literal})::{sql_type}");
    run_batched_update(pool, tenant_id, entity_name, table, op_id, &set_clause, None).await
}

/// `set_clause` is a raw `SET ...` fragment (already valid SQL — callers build it from
/// server-authored metadata only, same trust boundary as everywhere else in this crate);
/// `where_extra`, if given, further restricts which rows a batch picks up — used by
/// `migration::apply_widen_type` with an idempotent predicate (e.g.
/// `jsonb_typeof(t.data->'amount') = 'string'`) so a resumed/re-run pass only touches rows that
/// still need the transform, the same "already transformed rows just don't match the WHERE
/// anymore" idempotency the design's §4.3 examples rely on.
pub async fn run_batched_update(
    pool: &PgPool,
    tenant_id: Uuid,
    entity_name: &str,
    table: &str,
    op_id: &str,
    set_clause: &str,
    where_extra: Option<&str>,
) -> anyhow::Result<()> {
    let mut cursor = load_cursor(pool, tenant_id, entity_name, op_id)
        .await?
        .unwrap_or(Uuid::nil());

    let quoted_table = quote_qualified_ident(table);
    let extra = where_extra.map(|w| format!(" AND ({w})")).unwrap_or_default();
    let sql = format!(
        "WITH batch AS (SELECT id FROM {quoted_table} t WHERE id > $1{extra} ORDER BY id LIMIT {BATCH_SIZE}) \
         UPDATE {quoted_table} t SET {set_clause} FROM batch WHERE t.id = batch.id RETURNING t.id"
    );

    loop {
        let mut tx = pool.begin().await?;
        let ids: Vec<Uuid> = sqlx::query_scalar(&sql).bind(cursor).fetch_all(&mut *tx).await?;
        if ids.is_empty() {
            tx.commit().await?;
            break;
        }
        cursor = *ids.iter().max().expect("just checked non-empty");
        save_progress(&mut tx, tenant_id, entity_name, op_id, cursor, false).await?;
        tx.commit().await?;
        tokio::time::sleep(THROTTLE).await;
    }

    mark_completed(pool, tenant_id, entity_name, op_id).await?;
    Ok(())
}

async fn load_cursor(pool: &PgPool, tenant_id: Uuid, entity_name: &str, op_id: &str) -> anyhow::Result<Option<Uuid>> {
    let cursor: Option<Uuid> = sqlx::query_scalar(
        "SELECT cursor_id FROM reconciler_backfill_progress WHERE tenant_id = $1 AND entity_name = $2 AND op_id = $3",
    )
    .bind(tenant_id)
    .bind(entity_name)
    .bind(op_id)
    .fetch_optional(pool)
    .await?
    .flatten();
    Ok(cursor)
}

async fn save_progress(
    tx: &mut sqlx::Transaction<'_, sqlx::Postgres>,
    tenant_id: Uuid,
    entity_name: &str,
    op_id: &str,
    cursor: Uuid,
    completed: bool,
) -> anyhow::Result<()> {
    sqlx::query(
        "INSERT INTO reconciler_backfill_progress (tenant_id, entity_name, op_id, cursor_id, completed, updated_at)
         VALUES ($1, $2, $3, $4, $5, now())
         ON CONFLICT (tenant_id, entity_name, op_id) DO UPDATE
         SET cursor_id = EXCLUDED.cursor_id, completed = EXCLUDED.completed, updated_at = now()",
    )
    .bind(tenant_id)
    .bind(entity_name)
    .bind(op_id)
    .bind(cursor)
    .bind(completed)
    .execute(&mut **tx)
    .await?;
    Ok(())
}

async fn mark_completed(pool: &PgPool, tenant_id: Uuid, entity_name: &str, op_id: &str) -> anyhow::Result<()> {
    sqlx::query(
        "INSERT INTO reconciler_backfill_progress (tenant_id, entity_name, op_id, completed, updated_at)
         VALUES ($1, $2, $3, true, now())
         ON CONFLICT (tenant_id, entity_name, op_id) DO UPDATE SET completed = true, updated_at = now()",
    )
    .bind(tenant_id)
    .bind(entity_name)
    .bind(op_id)
    .execute(pool)
    .await?;
    Ok(())
}
