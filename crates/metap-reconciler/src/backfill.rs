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

/// Whether the rows a backfill touches for one `(tenant_id, entity)` reconcile belong
/// exclusively to `tenant_id`, or are spread across every tenant sharing one physical table.
/// This crate has no concept of `TenantStrategy` itself (that's `metap-control`, a layer up) —
/// the caller supplies this because only it knows which shape applies. Found live
/// (`metap-demo-waf/CLAUDE.md`'s 9th finding, `../metap-docs/docs/roadmap/84-*.md` item 3): a
/// `Schema`-strategy service reconciling its own shared table at boot always passes
/// `metap_control::PLATFORM_TENANT_ID` (a sentinel with zero real rows), and the batch backfill
/// used to filter by it unconditionally (`t.tenant_id = $2`) — real historical rows belonging to
/// any actual tenant were never reachable by that boot-time backfill; the query matched zero
/// rows *for the wrong reason* (not "nothing left to do", but "was never going to find
/// anything"), and `mark_completed` fired anyway, indistinguishable from genuine completion.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum BackfillScope {
    /// Every row this reconcile's table holds belongs to `tenant_id` — a `DedicatedDb` tenant's
    /// own database, or any other reconcile genuinely invoked per real tenant (e.g.
    /// `reconciler-orchestrator`'s per-`(tenant, entity)` fan-out). The historical, still-correct
    /// default.
    SingleTenant,
    /// The table is shared across many tenants and `tenant_id` is a sentinel identifying *this
    /// reconcile call*, not a row owner to filter by (`metap_control::PLATFORM_TENANT_ID` at a
    /// `Schema`-strategy service's own boot). A backfill scoped this way touches every row in the
    /// table regardless of which real tenant it belongs to; the progress ledger still keys off
    /// the passed-in `tenant_id` (the sentinel), representing "this whole shared table's
    /// backfill", not any one tenant's.
    AllTenants,
}

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
    scope: BackfillScope,
) -> anyhow::Result<()> {
    let quoted_col = quote_ident(column);
    let field_literal = quote_literal(source_field);
    let set_clause = format!("{quoted_col} = (t.data ->> {field_literal})::{sql_type}");
    run_batched_update(pool, tenant_id, entity_name, table, op_id, &set_clause, None, scope).await
}

/// `set_clause` is a raw `SET ...` fragment (already valid SQL — callers build it from
/// server-authored metadata only, same trust boundary as everywhere else in this crate);
/// `where_extra`, if given, further restricts which rows a batch picks up — used by
/// `migration::apply_widen_type` with an idempotent predicate (e.g.
/// `jsonb_typeof(t.data->'amount') = 'string'`) so a resumed/re-run pass only touches rows that
/// still need the transform, the same "already transformed rows just don't match the WHERE
/// anymore" idempotency the design's §4.3 examples rely on.
#[allow(clippy::too_many_arguments)]
pub async fn run_batched_update(
    pool: &PgPool,
    tenant_id: Uuid,
    entity_name: &str,
    table: &str,
    op_id: &str,
    set_clause: &str,
    where_extra: Option<&str>,
    scope: BackfillScope,
) -> anyhow::Result<()> {
    let mut cursor = load_cursor(pool, tenant_id, entity_name, op_id)
        .await?
        .unwrap_or(Uuid::nil());

    let quoted_table = quote_qualified_ident(table);
    let extra = where_extra.map(|w| format!(" AND ({w})")).unwrap_or_default();
    // `t.tenant_id = $2` — found live (`AUDIT_2.md`): this batch's own `SELECT` had no tenant
    // scoping at all, relying entirely on the convention "every dedicated table belongs to
    // exactly one `DedicatedDb` tenant" (documented in `CLAUDE.md`, never checked in code). Safe
    // today only because that convention has always held in practice; this makes it structurally
    // true instead — a caller reconciling a shared (`Schema`-strategy) table for the wrong tenant
    // now can't touch another tenant's rows even if that convention were ever violated.
    //
    // `BackfillScope::AllTenants` (`metap-demo-waf/CLAUDE.md`'s 9th finding) drops this filter
    // entirely instead of binding it to the sentinel `tenant_id` a `Schema`-strategy service's
    // boot-time reconcile always passes — that sentinel has zero real rows, so keeping the filter
    // for a genuinely shared table meant this batch matched nothing, every time, no matter how
    // much real data existed. Both branches still key the progress ledger off `tenant_id` as
    // given (unrelated to what the query itself scans).
    let sql = match scope {
        BackfillScope::SingleTenant => format!(
            "WITH batch AS (SELECT id FROM {quoted_table} t WHERE t.tenant_id = $2 AND id > $1{extra} \
             ORDER BY id LIMIT {BATCH_SIZE}) \
             UPDATE {quoted_table} t SET {set_clause} FROM batch WHERE t.id = batch.id RETURNING t.id"
        ),
        BackfillScope::AllTenants => format!(
            "WITH batch AS (SELECT id FROM {quoted_table} t WHERE id > $1{extra} \
             ORDER BY id LIMIT {BATCH_SIZE}) \
             UPDATE {quoted_table} t SET {set_clause} FROM batch WHERE t.id = batch.id RETURNING t.id"
        ),
    };

    loop {
        let mut tx = pool.begin().await?;
        let query = sqlx::query_scalar(&sql).bind(cursor);
        let query = match scope {
            BackfillScope::SingleTenant => query.bind(tenant_id),
            BackfillScope::AllTenants => query,
        };
        let ids: Vec<Uuid> = query.fetch_all(&mut *tx).await?;
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

/// `pub(crate)` (not private) — reused as-is by `migrate::copy_generic_records`, which needs the
/// exact same checkpoint-table shape for a different kind of op (`migrate::MIGRATE_OP_ID` instead
/// of a real `BackfillColumn`'s `op_id`), not a parallel copy of the same 3 queries.
pub(crate) async fn load_cursor(
    pool: &PgPool,
    tenant_id: Uuid,
    entity_name: &str,
    op_id: &str,
) -> anyhow::Result<Option<Uuid>> {
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

pub(crate) async fn save_progress(
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

pub(crate) async fn mark_completed(
    pool: &PgPool,
    tenant_id: Uuid,
    entity_name: &str,
    op_id: &str,
) -> anyhow::Result<()> {
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
