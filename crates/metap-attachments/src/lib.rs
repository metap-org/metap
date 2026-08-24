//! File attachment metadata — `metap-storage::ObjectStore`'s first real consumer anywhere in
//! this repo. Deliberately not a metadata-driven `EntityDefinition`: `record_id` can point at a
//! row in *any* entity's table (the shared `records` table, or any dedicated table-per-entity
//! table), which rules out a typed foreign key the way a normal `Reference` field gets — same
//! category as `policies`/`cron_jobs`/`workflow_events`, a plain platform table, not per-entity
//! JSONB.
//!
//! **Two storage mechanisms, not one** (project owner decision): every function here takes a
//! `table_name: &str` rather than hardcoding `"attachments"` — a caller passes the shared
//! default table for most entities, or a dedicated table (`ensure_dedicated_table`) for one
//! expecting heavy attachment volume, without this crate needing to know which. Both tables
//! share the exact same fixed 8-column shape (attachment metadata never varies per entity, so
//! this needs no reconcile()-style dynamic schema diffing the way per-entity `records` does).
//!
//! **Known tradeoff, not a bug**: unlike a real `Reference` field, nothing here blocks deleting
//! the record an attachment points at — `record_id` can't carry a DB-level FK across arbitrary
//! target tables, and no `metap-*` library crate (including `metap-crud`, which owns record
//! deletion) is allowed to know this concept exists. Deleting a referenced record leaves its
//! attachment rows pointing at nothing — the object-storage blob itself is untouched (no data
//! loss), just a dangling metadata reference. Revisit with a real cross-cutting guard if this
//! becomes a real problem, not preemptively.
//!
//! No HTTP — a plain library, same shape as `metap-cron`.

use chrono::{DateTime, Utc};
use sqlx::{PgExecutor, PgPool, Row};
use uuid::Uuid;

#[derive(Debug, Clone)]
pub struct AttachmentRecord {
    pub id: Uuid,
    pub tenant_id: Uuid,
    pub entity_name: String,
    pub record_id: Uuid,
    pub filename: String,
    pub key: String,
    pub size: i64,
    pub content_type: Option<String>,
    pub created_by: Option<Uuid>,
    pub created_at: DateTime<Utc>,
}

fn row_to_record(row: sqlx::postgres::PgRow) -> anyhow::Result<AttachmentRecord> {
    Ok(AttachmentRecord {
        id: row.try_get("id")?,
        tenant_id: row.try_get("tenant_id")?,
        entity_name: row.try_get("entity_name")?,
        record_id: row.try_get("record_id")?,
        filename: row.try_get("filename")?,
        key: row.try_get("key")?,
        size: row.try_get("size")?,
        content_type: row.try_get("content_type")?,
        created_by: row.try_get("created_by")?,
        created_at: row.try_get("created_at")?,
    })
}

/// SQL can't parameterize an identifier — every function below interpolates `table_name`
/// directly into its query, so this is checked once, centrally, on every call (cheap, and
/// mirrors `Router::validate_schema_name`'s same discipline for the same reason: untrusted-shaped
/// input gets checked at the boundary, not trusted because no caller misuses it today).
fn validate_table_name(table_name: &str) -> anyhow::Result<()> {
    let mut chars = table_name.chars();
    let starts_ok = chars.next().is_some_and(|c| c.is_ascii_lowercase());
    let rest_ok = chars.all(|c| c.is_ascii_lowercase() || c.is_ascii_digit() || c == '_');
    anyhow::ensure!(
        starts_ok && rest_ok,
        "invalid attachments table name {table_name:?} — must match ^[a-z][a-z0-9_]*$"
    );
    Ok(())
}

/// Creates a dedicated attachments table for one entity expecting heavy volume, if it doesn't
/// already exist — same fixed shape as the shared default `attachments` table
/// (`crates/migrations/0021_attachments.sql`), just under its own name so a high-volume entity's
/// attachment rows don't crowd every other entity's. Call once at boot, same pattern
/// `reconcile()` is called per entity today — idempotent (`IF NOT EXISTS`), safe to call on
/// every boot.
pub async fn ensure_dedicated_table(pool: &PgPool, table_name: &str) -> anyhow::Result<()> {
    validate_table_name(table_name)?;
    sqlx::query(&format!(
        "CREATE TABLE IF NOT EXISTS {table_name} (
           id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
           tenant_id    uuid NOT NULL,
           entity_name  text NOT NULL,
           record_id    uuid NOT NULL,
           filename     text NOT NULL,
           key          text NOT NULL,
           size         bigint NOT NULL,
           content_type text,
           created_by   uuid,
           created_at   timestamptz NOT NULL DEFAULT now()
         )"
    ))
    .execute(pool)
    .await?;
    sqlx::query(&format!(
        "CREATE INDEX IF NOT EXISTS {table_name}_tenant_entity_record_idx \
         ON {table_name} (tenant_id, entity_name, record_id)"
    ))
    .execute(pool)
    .await?;
    Ok(())
}

#[allow(clippy::too_many_arguments)]
pub async fn create_attachment<'e>(
    executor: impl PgExecutor<'e>,
    table_name: &str,
    tenant_id: Uuid,
    entity_name: &str,
    record_id: Uuid,
    filename: &str,
    key: &str,
    size: i64,
    content_type: Option<&str>,
    created_by: Option<Uuid>,
) -> anyhow::Result<AttachmentRecord> {
    validate_table_name(table_name)?;
    let row = sqlx::query(&format!(
        "INSERT INTO {table_name} (tenant_id, entity_name, record_id, filename, key, size, content_type, created_by) \
         VALUES ($1, $2, $3, $4, $5, $6, $7, $8) \
         RETURNING id, tenant_id, entity_name, record_id, filename, key, size, content_type, created_by, created_at"
    ))
    .bind(tenant_id)
    .bind(entity_name)
    .bind(record_id)
    .bind(filename)
    .bind(key)
    .bind(size)
    .bind(content_type)
    .bind(created_by)
    .fetch_one(executor)
    .await?;
    row_to_record(row)
}

pub async fn list_attachments<'e>(
    executor: impl PgExecutor<'e>,
    table_name: &str,
    tenant_id: Uuid,
    entity_name: &str,
    record_id: Uuid,
) -> anyhow::Result<Vec<AttachmentRecord>> {
    validate_table_name(table_name)?;
    let rows = sqlx::query(&format!(
        "SELECT id, tenant_id, entity_name, record_id, filename, key, size, content_type, created_by, created_at \
         FROM {table_name} WHERE tenant_id = $1 AND entity_name = $2 AND record_id = $3 \
         ORDER BY created_at DESC"
    ))
    .bind(tenant_id)
    .bind(entity_name)
    .bind(record_id)
    .fetch_all(executor)
    .await?;
    rows.into_iter().map(row_to_record).collect()
}

pub async fn get_attachment<'e>(
    executor: impl PgExecutor<'e>,
    table_name: &str,
    tenant_id: Uuid,
    id: Uuid,
) -> anyhow::Result<Option<AttachmentRecord>> {
    validate_table_name(table_name)?;
    let row = sqlx::query(&format!(
        "SELECT id, tenant_id, entity_name, record_id, filename, key, size, content_type, created_by, created_at \
         FROM {table_name} WHERE tenant_id = $1 AND id = $2"
    ))
    .bind(tenant_id)
    .bind(id)
    .fetch_optional(executor)
    .await?;
    row.map(row_to_record).transpose()
}

pub async fn delete_attachment<'e>(
    executor: impl PgExecutor<'e>,
    table_name: &str,
    tenant_id: Uuid,
    id: Uuid,
) -> anyhow::Result<()> {
    validate_table_name(table_name)?;
    sqlx::query(&format!("DELETE FROM {table_name} WHERE tenant_id = $1 AND id = $2"))
        .bind(tenant_id)
        .bind(id)
        .execute(executor)
        .await?;
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn validate_table_name_accepts_normal_identifiers() {
        assert!(validate_table_name("attachments").is_ok());
        assert!(validate_table_name("jira_issue_attachments").is_ok());
    }

    #[test]
    fn validate_table_name_rejects_injection_shaped_input() {
        assert!(validate_table_name("attachments; DROP TABLE users;--").is_err());
        assert!(validate_table_name("Attachments").is_err());
        assert!(validate_table_name("").is_err());
        assert!(validate_table_name("1attachments").is_err());
    }
}
