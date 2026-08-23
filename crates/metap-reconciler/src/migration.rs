//! §4 — Data Plane Data Evolution. `diff()`/`executor` (§5) already cover plain add/rename
//! (a `RenameColumn`/`AddColumn` `DdlOp` never fails against existing data). What's left is the
//! op that *can* fail against dirty data — widening a field's type — plus the declarative,
//! no-code-hook op set §4.2 calls for. No custom transform functions: every op here maps to a
//! fixed, generated SQL statement (§4.3), never arbitrary caller code.

use serde_json::Value;
use sqlx::PgPool;

use crate::schema::FkSpec;
use crate::sqlfmt::{quote_ident, quote_literal, quote_qualified_ident};
use crate::{backfill, quarantine};

#[derive(Debug, Clone)]
pub enum MigrationOp {
    RenameField {
        from: String,
        to: String,
    },
    /// `default: None` leaves the field simply absent on pre-existing rows (lazy — the next
    /// write populates it); required-with-no-default is a metadata-authoring-time validation
    /// concern (`metap-crud`'s field validator), not something this crate re-checks.
    AddField {
        name: String,
        default: Option<Value>,
    },
    /// The one op that can fail against existing data — `to_sql_type` must be a type
    /// `pg_input_is_valid` (Postgres 16+) understands, e.g. `"numeric(18,4)"`, `"timestamptz"`.
    /// Assumes the field's *current* JSON representation is a string (`jsonb_typeof = 'string'`)
    /// — matches §4.3's own `string → numeric` example; widening from a non-string JSON type
    /// isn't a case this op covers.
    WidenType {
        field: String,
        to_sql_type: String,
    },
    DropField {
        field: String,
        keep_data: bool,
    },
    RemoveEnum {
        field: String,
        value: String,
        remap_to: String,
    },
}

/// §4.4 — what to do when `preflight` finds rows that would fail the op.
#[derive(Debug, Clone)]
pub enum QuarantinePolicy {
    /// The default — halt, do nothing to the data, and let the caller surface `bad_rows` to a
    /// human before retrying.
    Block,
    /// Every row gets `fallback` instead of failing the cast — no row is ever removed.
    /// `fallback` must be a JSON value already of the *target* scalar type (a JSON number for
    /// a `WidenType` targeting `numeric`, not a JSON string of digits) — it is embedded
    /// directly into a `::{to_sql_type}` cast, not round-tripped through `jsonb` first.
    Coerce { fallback: Value },
    /// Bad rows are moved to `{table}_quarantine` (`quarantine::quarantine_bad_rows`) before the
    /// transform runs, so the transform itself only ever touches clean rows.
    Quarantine,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct PreflightReport {
    pub bad_rows: i64,
}

/// §4.4 — "scan đếm TRƯỚC khi migrate, không nổ giữa 10M": a pure `SELECT count(*)`, no
/// mutation. Only `WidenType` can ever report `bad_rows > 0`; every other op is safe by
/// construction (rename/add/drop never fail a cast, `remove_enum` always has an explicit
/// `remap_to`).
pub async fn preflight(pool: &PgPool, table: &str, op: &MigrationOp) -> anyhow::Result<PreflightReport> {
    let Some(predicate) = bad_row_predicate(op) else {
        return Ok(PreflightReport { bad_rows: 0 });
    };
    let quoted_table = quote_qualified_ident(table);
    let bad_rows: i64 = sqlx::query_scalar(&format!("SELECT count(*) FROM {quoted_table} t WHERE {predicate}"))
        .fetch_one(pool)
        .await?;
    Ok(PreflightReport { bad_rows })
}

/// §4.4's "orphan ref" preflight — separate from `MigrationOp` because it belongs to a `FkSpec`
/// (a `Reference` field's constraint), not a data-evolution op. A caller wanting non-`Block`
/// handling for orphaned rows before adding/validating a FK runs this (and
/// `quarantine::quarantine_bad_rows`) *before* `reconcile()` — the default (no preflight run at
/// all) is still safe: `executor`'s `ValidateForeignKey` simply fails cleanly with Postgres's
/// own `foreign_key_violation` error, which is Policy::Block in every way that matters (nothing
/// touched, error surfaced, entity left for a human).
pub async fn preflight_fk_orphans(pool: &PgPool, table: &str, fk: &FkSpec) -> anyhow::Result<PreflightReport> {
    let predicate = fk_orphan_predicate(fk);
    let quoted_table = quote_qualified_ident(table);
    let bad_rows: i64 = sqlx::query_scalar(&format!("SELECT count(*) FROM {quoted_table} t WHERE {predicate}"))
        .fetch_one(pool)
        .await?;
    Ok(PreflightReport { bad_rows })
}

fn fk_orphan_predicate(fk: &FkSpec) -> String {
    let col = quote_ident(&fk.column);
    let ref_table = quote_qualified_ident(&fk.ref_table);
    let ref_col = quote_ident(&fk.ref_column);
    format!("t.{col} IS NOT NULL AND NOT EXISTS (SELECT 1 FROM {ref_table} r WHERE r.{ref_col} = t.{col})")
}

fn bad_row_predicate(op: &MigrationOp) -> Option<String> {
    match op {
        MigrationOp::WidenType { field, to_sql_type } => {
            let f = quote_literal(field);
            let ty = quote_literal(to_sql_type);
            Some(format!(
                "jsonb_typeof(t.data -> {f}) = 'string' AND NOT pg_input_is_valid(t.data ->> {f}, {ty})"
            ))
        }
        _ => None,
    }
}

#[derive(Debug, Clone, Copy, Default)]
pub struct MigrationOutcome {
    pub quarantined: i64,
    pub skipped_referenced: i64,
}

/// Runs one `MigrationOp` end to end: preflight → apply `policy` → batched transform (only the
/// rows still needing it — idempotent, safe to call again). `migration_id` identifies this run
/// in `reconciler_backfill_progress`/quarantine rows (checkpoint/resume key) — callers should
/// pass something stable across retries of the *same* logical migration (e.g. a hash of the op).
pub async fn run_migration(
    pool: &PgPool,
    tenant_id: uuid::Uuid,
    entity_name: &str,
    table: &str,
    op: &MigrationOp,
    policy: &QuarantinePolicy,
    migration_id: &str,
) -> anyhow::Result<MigrationOutcome> {
    let report = preflight(pool, table, op).await?;
    let mut outcome = MigrationOutcome::default();

    if report.bad_rows > 0 {
        match policy {
            QuarantinePolicy::Block => {
                anyhow::bail!(
                    "migration blocked: {} row(s) would fail this op (policy: Block)",
                    report.bad_rows
                );
            }
            QuarantinePolicy::Quarantine => {
                if let Some(predicate) = bad_row_predicate(op) {
                    let q = quarantine::quarantine_bad_rows(
                        pool,
                        table,
                        &predicate,
                        migration_id,
                        "migration_preflight_failed",
                    )
                    .await?;
                    outcome.quarantined = q.quarantined;
                    outcome.skipped_referenced = q.skipped_referenced;
                }
            }
            QuarantinePolicy::Coerce { .. } => {
                // No separate step — `apply_sql` below builds the fallback directly into the
                // transform's `SET` clause, so a would-fail row is coerced in the same pass
                // instead of needing to be found and touched twice.
            }
        }
    }

    let op_id = format!("migration:{table}:{migration_id}");
    if let Some((set_clause, where_extra)) = apply_sql(op, policy) {
        backfill::run_batched_update(
            pool,
            tenant_id,
            entity_name,
            table,
            &op_id,
            &set_clause,
            where_extra.as_deref(),
        )
        .await?;
    }

    Ok(outcome)
}

/// Builds the `(SET clause, optional extra WHERE)` for `backfill::run_batched_update` — `None`
/// for an op with nothing to backfill (a `DropField { keep_data: true }` only ever needs a
/// metadata change, no data touched).
fn apply_sql(op: &MigrationOp, policy: &QuarantinePolicy) -> Option<(String, Option<String>)> {
    match op {
        MigrationOp::RenameField { from, to } => {
            let from_lit = quote_literal(from);
            let to_lit = quote_literal(to);
            Some((
                format!("data = (t.data - {from_lit}) || jsonb_build_object({to_lit}, t.data -> {from_lit})"),
                Some(format!("t.data ? {from_lit}")),
            ))
        }
        MigrationOp::AddField { name, default } => {
            let default = default.as_ref()?;
            let name_lit = quote_literal(name);
            Some((
                format!(
                    "data = jsonb_set(t.data, ARRAY[{name_lit}], {}::jsonb, true)",
                    quote_literal(&default.to_string())
                ),
                Some(format!("NOT (t.data ? {name_lit})")),
            ))
        }
        MigrationOp::WidenType { field, to_sql_type } => {
            let f = quote_literal(field);
            let cast_expr = match policy {
                QuarantinePolicy::Coerce { fallback } => format!(
                    "CASE WHEN pg_input_is_valid(t.data ->> {f}, {ty}) THEN (t.data ->> {f})::{sql_type} \
                     ELSE {fallback}::{sql_type} END",
                    ty = quote_literal(to_sql_type),
                    sql_type = to_sql_type,
                    fallback = quote_literal(&fallback.to_string()),
                ),
                _ => format!("(t.data ->> {f})::{to_sql_type}"),
            };
            Some((
                format!("data = jsonb_set(t.data, ARRAY[{f}], to_jsonb(({cast_expr})))"),
                Some(format!("jsonb_typeof(t.data -> {f}) = 'string'")),
            ))
        }
        MigrationOp::DropField { field, keep_data } => {
            if *keep_data {
                return None;
            }
            let f = quote_literal(field);
            Some((format!("data = t.data - {f}"), Some(format!("t.data ? {f}"))))
        }
        MigrationOp::RemoveEnum { field, value, remap_to } => {
            let f = quote_literal(field);
            Some((
                format!(
                    "data = jsonb_set(t.data, ARRAY[{f}], to_jsonb({}::text))",
                    quote_literal(remap_to)
                ),
                Some(format!("t.data ->> {f} = {}", quote_literal(value))),
            ))
        }
    }
}
