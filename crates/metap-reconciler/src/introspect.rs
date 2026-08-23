//! `introspect(actual) -> Option<PhysicalSchema>` — the actual-state half of the reconcile
//! pipeline (`docs/multi-tenant-platform-design.md` §5.2), read straight from `pg_catalog`.
//! `None` means the table doesn't exist yet (`diff()`'s step 0: emit `CreateTable`).

use std::collections::BTreeMap;

use sqlx::{PgPool, Row};

use crate::compile::FRAMEWORK_COLUMNS;
use crate::schema::{ColumnOrigin, ColumnSpec, FkSpec, IndexSpec, OnDelete, PhysicalSchema, UniqueSpec};

pub async fn introspect(
    pool: &PgPool,
    tenant_id: uuid::Uuid,
    entity_name: &str,
    table: &str,
) -> anyhow::Result<Option<PhysicalSchema>> {
    let exists: bool = sqlx::query_scalar("SELECT to_regclass('public.' || $1) IS NOT NULL")
        .bind(table)
        .fetch_one(pool)
        .await?;
    if !exists {
        return Ok(None);
    }

    let framework_names: std::collections::HashSet<&str> = FRAMEWORK_COLUMNS.iter().map(|(name, ..)| *name).collect();
    let mut columns = BTreeMap::new();
    let column_rows = sqlx::query(
        "SELECT column_name, data_type, is_nullable, character_maximum_length, numeric_precision, numeric_scale
         FROM information_schema.columns WHERE table_schema = 'public' AND table_name = $1",
    )
    .bind(table)
    .fetch_all(pool)
    .await?;
    for row in column_rows {
        let name: String = row.try_get("column_name")?;
        let data_type: String = row.try_get("data_type")?;
        let is_nullable: String = row.try_get("is_nullable")?;
        let char_len: Option<i32> = row.try_get("character_maximum_length")?;
        let numeric_precision: Option<i32> = row.try_get("numeric_precision")?;
        let numeric_scale: Option<i32> = row.try_get("numeric_scale")?;
        let sql_type = pg_type_to_short(&data_type, char_len, numeric_precision, numeric_scale);
        let origin = if framework_names.contains(name.as_str()) {
            ColumnOrigin::Framework
        } else {
            let backfilled = is_backfill_completed(pool, tenant_id, entity_name, &backfill_op_id(table, &name)).await?;
            ColumnOrigin::Generated {
                source_field: name.clone(),
                backfilled,
            }
        };
        columns.insert(
            name,
            ColumnSpec {
                sql_type,
                nullable: is_nullable == "YES",
                origin,
            },
        );
    }

    let mut indexes = BTreeMap::new();
    let index_rows = sqlx::query(
        "SELECT ix.relname AS index_name, pg_get_indexdef(ix.oid) AS indexdef, i.indisvalid, i.indisunique, am.amname
         FROM pg_index i
         JOIN pg_class ix ON ix.oid = i.indexrelid
         JOIN pg_class tbl ON tbl.oid = i.indrelid
         JOIN pg_am am ON am.oid = ix.relam
         JOIN pg_namespace ns ON ns.oid = tbl.relnamespace
         WHERE tbl.relname = $1 AND ns.nspname = 'public' AND NOT i.indisprimary",
    )
    .bind(table)
    .fetch_all(pool)
    .await?;
    for row in index_rows {
        let index_name: String = row.try_get("index_name")?;
        let indexdef: String = row.try_get("indexdef")?;
        let valid: bool = row.try_get("indisvalid")?;
        let unique: bool = row.try_get("indisunique")?;
        let amname: String = row.try_get("amname")?;
        let expression = extract_index_expression(&indexdef);
        indexes.insert(
            index_name,
            IndexSpec {
                expression,
                unique,
                using: (amname != "btree").then_some(amname),
                valid,
            },
        );
    }

    let mut foreign_keys = BTreeMap::new();
    let mut uniques = BTreeMap::new();
    let constraint_rows = sqlx::query(
        "SELECT c.conname, c.contype, c.convalidated,
                array(SELECT attname FROM pg_attribute WHERE attrelid = c.conrelid AND attnum = ANY(c.conkey)) AS cols,
                ref.relname AS ref_table,
                array(SELECT attname FROM pg_attribute WHERE attrelid = c.confrelid AND attnum = ANY(c.confkey)) AS ref_cols,
                c.confdeltype
         FROM pg_constraint c
         JOIN pg_class tbl ON tbl.oid = c.conrelid
         JOIN pg_namespace ns ON ns.oid = tbl.relnamespace
         LEFT JOIN pg_class ref ON ref.oid = c.confrelid
         WHERE tbl.relname = $1 AND ns.nspname = 'public' AND c.contype IN ('f', 'u')",
    )
    .bind(table)
    .fetch_all(pool)
    .await?;
    for row in constraint_rows {
        let conname: String = row.try_get("conname")?;
        let contype: String = row.try_get("contype")?;
        match contype.as_str() {
            "f" => {
                let cols: Vec<String> = row.try_get("cols")?;
                let ref_table: String = row.try_get("ref_table")?;
                let ref_cols: Vec<String> = row.try_get("ref_cols")?;
                let convalidated: bool = row.try_get("convalidated")?;
                let confdeltype: String = row.try_get("confdeltype")?;
                foreign_keys.insert(
                    conname,
                    FkSpec {
                        column: cols.into_iter().next().unwrap_or_default(),
                        ref_table,
                        ref_column: ref_cols.into_iter().next().unwrap_or_default(),
                        on_delete: on_delete_from_char(&confdeltype),
                        validated: convalidated,
                    },
                );
            }
            "u" => {
                let cols: Vec<String> = row.try_get("cols")?;
                uniques.insert(conname, UniqueSpec { columns: cols });
            }
            _ => {}
        }
    }

    Ok(Some(PhysicalSchema {
        table: table.to_string(),
        columns,
        indexes,
        foreign_keys,
        uniques,
    }))
}

pub fn backfill_op_id(table: &str, column: &str) -> String {
    format!("backfill:{table}:{column}")
}

async fn is_backfill_completed(
    pool: &PgPool,
    tenant_id: uuid::Uuid,
    entity_name: &str,
    op_id: &str,
) -> anyhow::Result<bool> {
    let completed: Option<bool> = sqlx::query_scalar(
        "SELECT completed FROM reconciler_backfill_progress
         WHERE tenant_id = $1 AND entity_name = $2 AND op_id = $3",
    )
    .bind(tenant_id)
    .bind(entity_name)
    .bind(op_id)
    .fetch_optional(pool)
    .await?;
    Ok(completed.unwrap_or(false))
}

fn on_delete_from_char(confdeltype: &str) -> OnDelete {
    match confdeltype {
        "c" => OnDelete::Cascade,
        "n" => OnDelete::SetNull,
        _ => OnDelete::Restrict,
    }
}

/// Reconstructs the short type string `compile()` uses (`"varchar(120)"`, `"timestamptz"`,
/// `"numeric(18,4)"`, ...) from `information_schema.columns`' verbose SQL-standard names, so
/// both sides of `diff()`'s column comparison speak the same vocabulary.
fn pg_type_to_short(
    data_type: &str,
    char_len: Option<i32>,
    numeric_precision: Option<i32>,
    numeric_scale: Option<i32>,
) -> String {
    match data_type {
        "character varying" => match char_len {
            Some(n) => format!("varchar({n})"),
            None => "varchar".to_string(),
        },
        "timestamp with time zone" => "timestamptz".to_string(),
        "numeric" => match (numeric_precision, numeric_scale) {
            (Some(p), Some(s)) => format!("numeric({p},{s})"),
            _ => "numeric".to_string(),
        },
        other => other.to_string(),
    }
}

/// `pg_get_indexdef` returns the *whole* `CREATE INDEX ...` statement — this crate only wants
/// the column/expression list (plus any trailing opclass, e.g. `gin_trgm_ops`, which sits
/// *outside* the expression's own parens). Format is always
/// `... USING <method> (<expr>)[ <opclass>]`; per-entity tables never use partial indexes (one
/// table = one entity already, unlike the shared `records` table's `WHERE entity = ...`), so
/// there's no `WHERE` clause to strip and nothing after the closing paren but an optional
/// opclass — safe to just take everything from the first `(` after `USING <method> ` to the end
/// of the string. `normalize_expr` strips every paren anyway, so exact bracket-matching here
/// would be work spent for no comparison benefit.
fn extract_index_expression(indexdef: &str) -> String {
    let Some(using_pos) = indexdef.find(" USING ") else {
        return indexdef.to_string();
    };
    let after_using = &indexdef[using_pos + " USING ".len()..];
    match after_using.find('(') {
        Some(paren_start) => after_using[paren_start..].trim().to_string(),
        None => after_using.trim().to_string(),
    }
}
