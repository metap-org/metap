//! Mirrors `packages/core/src/core/metadata/index-reconciler.ts`. Never lets a DB hiccup
//! at boot become a crash (same stance as `metadata_drift.rs`); `CREATE INDEX
//! CONCURRENTLY` never blocks concurrent reads/writes on `records`, so this is safe to run
//! on every boot in every environment.

use metap_metadata::EntitySummary;
use sqlx::PgPool;

fn build_index_name(entity_name: &str, field_name: &str, kind: &str) -> String {
    let prefix = match kind {
        "uniq" => "uniq_records",
        "gin" => "gin_records",
        _ => "idx_records",
    };
    format!("{prefix}_{}_{field_name}", entity_name.replace('.', "_"))
}

/// Postgres DDL statements (`CREATE INDEX`) do not support bind parameters at all — unlike
/// a `SELECT`, `CREATE INDEX ... WHERE entity = $1` fails outright. `entity_name`/
/// `field_name` are safe to inline as quoted literals here only because they come
/// exclusively from server-authored, `MetadataCompiler`-validated metadata, never from
/// request input.
fn quote_literal(value: &str) -> String {
    format!("'{}'", value.replace('\'', "''"))
}

fn quote_identifier(value: &str) -> String {
    format!("\"{}\"", value.replace('"', "\"\""))
}

pub async fn reconcile(pool: &PgPool, entities: &[EntitySummary]) {
    if let Err(err) = reconcile_inner(pool, entities).await {
        eprintln!("index: reconcile skipped, could not reach the database: {err:#}");
    }
}

async fn reconcile_inner(pool: &PgPool, entities: &[EntitySummary]) -> anyhow::Result<()> {
    for entity in entities {
        for field in &entity.fields {
            if field.indexed.unwrap_or(false) {
                ensure_index(pool, &entity.name, &field.name, false).await?;
            }
            if field.unique.unwrap_or(false) {
                ensure_index(pool, &entity.name, &field.name, true).await?;
            }
            if field.search_mode.as_deref() == Some("fts") {
                ensure_gin_index(pool, &entity.name, &field.name).await?;
            }
        }
    }
    Ok(())
}

async fn index_exists(pool: &PgPool, index_name: &str) -> anyhow::Result<bool> {
    let row: Option<i32> = sqlx::query_scalar("SELECT 1 FROM pg_indexes WHERE indexname = $1")
        .bind(index_name)
        .fetch_optional(pool)
        .await?;
    Ok(row.is_some())
}

async fn ensure_index(
    pool: &PgPool,
    entity_name: &str,
    field_name: &str,
    unique: bool,
) -> anyhow::Result<()> {
    let index_name = build_index_name(entity_name, field_name, if unique { "uniq" } else { "idx" });

    // Check first rather than relying solely on IF NOT EXISTS so we only log (and only pay
    // for a CONCURRENTLY build) when something actually changes.
    if index_exists(pool, &index_name).await? {
        return Ok(());
    }

    let field_literal = quote_literal(field_name);
    let entity_literal = quote_literal(entity_name);
    // Must match QueryPlanner's/condition_to_sql's field_expression() exactly
    // (jsonb_extract_path_text, not the `->>` operator) — Postgres only uses an expression
    // index when the query's expression is syntactically identical to the indexed one; see
    // docs/architectures/09-adr.md's "Index expression must match the query expression
    // exactly" entry.
    let columns = if unique {
        format!("tenant_id, (jsonb_extract_path_text(data, {field_literal}))")
    } else {
        format!("(jsonb_extract_path_text(data, {field_literal}))")
    };
    let unique_keyword = if unique { "UNIQUE " } else { "" };

    let sql = format!(
        "CREATE {unique_keyword}INDEX CONCURRENTLY IF NOT EXISTS {} \
         ON records ({columns}) WHERE entity = {entity_literal} AND deleted = false",
        quote_identifier(&index_name)
    );
    sqlx::query(&sql).execute(pool).await?;

    eprintln!(
        "index: created (entity={entity_name}, field={field_name}, unique={unique}, index={index_name})"
    );
    Ok(())
}

async fn ensure_gin_index(pool: &PgPool, entity_name: &str, field_name: &str) -> anyhow::Result<()> {
    let index_name = build_index_name(entity_name, field_name, "gin");

    if index_exists(pool, &index_name).await? {
        return Ok(());
    }

    let field_literal = quote_literal(field_name);
    let entity_literal = quote_literal(entity_name);

    // Must match QueryPlanner's to_tsvector('simple', jsonb_extract_path_text(data, field))
    // expression exactly — same reasoning as ensure_index above.
    let sql = format!(
        "CREATE INDEX CONCURRENTLY IF NOT EXISTS {} \
         ON records USING GIN (to_tsvector('simple', jsonb_extract_path_text(data, {field_literal}))) \
         WHERE entity = {entity_literal} AND deleted = false",
        quote_identifier(&index_name)
    );
    sqlx::query(&sql).execute(pool).await?;

    eprintln!("index: created (entity={entity_name}, field={field_name}, index={index_name})");
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn index_name_replaces_dots_and_uses_the_right_prefix() {
        assert_eq!(build_index_name("crm.customers", "email", "idx"), "idx_records_crm_customers_email");
        assert_eq!(build_index_name("crm.customers", "code", "uniq"), "uniq_records_crm_customers_code");
        assert_eq!(build_index_name("crm.customers", "name", "gin"), "gin_records_crm_customers_name");
    }

    #[test]
    fn quote_literal_escapes_single_quotes() {
        assert_eq!(quote_literal("o'brien"), "'o''brien'");
    }

    #[test]
    fn quote_identifier_escapes_double_quotes() {
        assert_eq!(quote_identifier("weird\"name"), "\"weird\"\"name\"");
    }
}
