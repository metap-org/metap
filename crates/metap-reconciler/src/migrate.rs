//! Migration path for an entity ALREADY LIVE on the shared generic `records` table that needs to
//! move onto its own dedicated table (`docs/features/12-migration-generic-to-dedicated-table.md`)
//! — distinct from `docs/features/04-table-per-entity.md`, which is about an entity created
//! directly on a dedicated table from day one. `reconcile()` already builds/maintains the
//! *target* table's structure (columns/indexes/FKs/triggers) for either case identically; this
//! module adds the one thing that case doesn't need: moving rows that already exist on a
//! *different* physical table onto it.
//!
//! **Downtime-acceptable by design** (the brief's "Quyết định cơ chế", 2026-09-06): no
//! dual-write, no shadow-read. The caller is responsible for stopping the service that owns
//! `entity` before calling [`migrate_generic_to_dedicated`] — this module assumes no concurrent
//! writer to `source_table` for the migrated `(tenant, entity)`, the same precondition
//! `backfill::run_batched_update` already assumes for a plain in-place backfill. That decision is
//! explicitly temporary (revisit once real production data exists — see the brief), which is why
//! this stays a separate one-shot function rather than folded into `reconcile`/`executor`'s
//! always-safe-to-re-run machinery.
//!
//! Not reusing `backfill::run_batched_update` (an explicit non-goal in the brief): that function
//! transforms rows *in place* on one table; this one INSERT-copies rows from one table to a
//! different one, which needs its own SQL shape — but it reuses `backfill`'s checkpoint-table
//! helpers (`load_cursor`/`save_progress`/`mark_completed` against
//! `reconciler_backfill_progress`) under a dedicated `op_id` ([`MIGRATE_OP_ID`]), so a crash mid-copy
//! self-heals with the exact same keyset-cursor mechanism, not a second one.
//!
//! Two steps a caller still owns after this returns (deliberately outside this crate's boundary,
//! same "commit_metadata GATE" reasoning as `executor`'s doc comment): flipping
//! `EntityDefinition.table_name` in their own source code to
//! [`crate::qualified_table_name_for`] (a code change, not data-driven — no `metap-*` crate can
//! rewrite a downstream binary's own source), and restarting the service.

use sqlx::{PgPool, Row};
use uuid::Uuid;

use metap_metadata::EntityDefinition;

use crate::backfill::{load_cursor, mark_completed, save_progress};
use crate::reconcile::{self, ReconcileOutcome};
use crate::sqlfmt::{quote_literal, quote_qualified_ident};

const BATCH_SIZE: i64 = 5000;
const THROTTLE: std::time::Duration = std::time::Duration::from_millis(20);

/// Fixed `op_id` this module's checkpoint rows use in `reconciler_backfill_progress` — distinct
/// from any real `BackfillColumn` op's own `op_id` (a field name) so the two kinds of progress row
/// for the same `(tenant, entity)` can never collide.
pub const MIGRATE_OP_ID: &str = "migrate_generic_to_dedicated";

#[derive(Debug, Clone, Copy, Default, PartialEq, Eq)]
pub struct CopySummary {
    /// Rows the source query actually returned across every batch — advances the resume cursor
    /// regardless of how many of them the destination's `ON CONFLICT (id) DO NOTHING` accepted,
    /// so a resumed run can never loop forever re-fetching a batch it already fully applied.
    pub rows_scanned: i64,
}

/// Checkpointed batch copy of one `(tenant, entity)`'s rows from `source_table` (a generic table
/// like `records`, filtered by its `entity` discriminator column) into `dest_table` — keyset-
/// paginated (`id > cursor`, no `OFFSET`), same four properties as
/// `backfill::run_batched_update`: no concurrent-writer assumption beyond what's documented above,
/// the checkpoint is saved in the *same transaction* as the batch insert (a crash between them is
/// impossible), a small sleep between batches so this never starves autovacuum/replication, and
/// cancellation is left to the caller.
///
/// One SQL statement per batch does both the copy and the id list the cursor advances by: `batch`
/// selects the next page from `source_table`, `ins` (a data-modifying CTE, always executed once
/// referenced in a `FROM`/`JOIN` — unlike a CTE only named in the `SELECT` list, which Postgres is
/// free to skip entirely) inserts it into `dest_table`, and the final `SELECT ... FROM batch LEFT
/// JOIN ins` returns every id `batch` fetched, whether or not `ins` actually wrote it — so a row
/// `ON CONFLICT` skipped (already present from a prior partial run) still advances the cursor
/// instead of being fetched again forever.
pub async fn copy_generic_records(
    pool: &PgPool,
    tenant_id: Uuid,
    entity_name: &str,
    source_table: &str,
    dest_table: &str,
) -> anyhow::Result<CopySummary> {
    let mut cursor = load_cursor(pool, tenant_id, entity_name, MIGRATE_OP_ID)
        .await?
        .unwrap_or(Uuid::nil());

    let quoted_source = quote_qualified_ident(source_table);
    let quoted_dest = quote_qualified_ident(dest_table);
    let entity_literal = quote_literal(entity_name);
    let sql = format!(
        "WITH batch AS (\
             SELECT id, tenant_id, code, status, data, version, deleted, created_at, updated_at, created_by, updated_by \
             FROM {quoted_source} \
             WHERE tenant_id = $2 AND entity = {entity_literal} AND id > $1 \
             ORDER BY id LIMIT {BATCH_SIZE}\
         ), ins AS (\
             INSERT INTO {quoted_dest} \
                 (id, tenant_id, code, status, data, version, deleted, created_at, updated_at, created_by, updated_by) \
             SELECT id, tenant_id, code, status, data, version, deleted, created_at, updated_at, created_by, updated_by \
             FROM batch \
             ON CONFLICT (id) DO NOTHING \
             RETURNING id\
         ) \
         SELECT b.id FROM batch b LEFT JOIN ins i ON i.id = b.id"
    );

    let mut rows_scanned: i64 = 0;
    loop {
        let mut tx = pool.begin().await?;
        let rows = sqlx::query(&sql).bind(cursor).bind(tenant_id).fetch_all(&mut *tx).await?;
        if rows.is_empty() {
            tx.commit().await?;
            break;
        }

        let ids: Vec<Uuid> = rows
            .iter()
            .map(|row| row.try_get::<Uuid, _>("id"))
            .collect::<Result<_, _>>()?;
        rows_scanned += ids.len() as i64;
        cursor = *ids.iter().max().expect("just checked non-empty");
        save_progress(&mut tx, tenant_id, entity_name, MIGRATE_OP_ID, cursor, false).await?;
        tx.commit().await?;
        tokio::time::sleep(THROTTLE).await;
    }

    mark_completed(pool, tenant_id, entity_name, MIGRATE_OP_ID).await?;
    Ok(CopySummary { rows_scanned })
}

#[derive(Debug, Clone)]
pub struct MigrateOutcome {
    /// The dedicated table's fully-qualified name (`entities.<mangled_name>`) — what a caller's
    /// own `EntityDefinition.table_name` must be updated to before restarting the service.
    pub table: String,
    pub reconcile: ReconcileOutcome,
    pub copy: CopySummary,
}

/// The full one-shot migration path: (a) `reconcile()` builds/updates `entity`'s dedicated table
/// exactly as it would for a brand-new table-per-entity entity (unmodified — this module adds
/// nothing to that mechanism), then (b) [`copy_generic_records`] moves every existing row for
/// `(tenant, entity)` off `source_table` (typically `"records"`) onto it. See this module's own
/// doc comment for the two steps a caller still owns after this returns (flipping
/// `EntityDefinition.table_name` in source and restarting) and the no-concurrent-writer
/// precondition.
pub async fn migrate_generic_to_dedicated(
    pool: &PgPool,
    tenant_id: Uuid,
    entity: &EntityDefinition,
    source_table: &str,
) -> anyhow::Result<MigrateOutcome> {
    let reconciled = reconcile::reconcile(pool, tenant_id, entity, &[]).await?;
    let copy = copy_generic_records(pool, tenant_id, &entity.name, source_table, &reconciled.table).await?;
    Ok(MigrateOutcome {
        table: reconciled.table.clone(),
        reconcile: reconciled,
        copy,
    })
}
