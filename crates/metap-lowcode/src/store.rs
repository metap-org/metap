//! Postgres-backed draft/publish/rollback storage for DB-authored entity metadata
//! (`docs/roadmap.md` Phase 11 / Phase A sub-project 1, retargeted from
//! `docs/low-code-metadata-storage-design.md`'s TS-era spec). Plain `&PgPool` functions, same
//! style as `metap_cron::store`/`metap_peripherals::preferences` — no pluggable-storage
//! requirement here.
//!
//! `low_code_entity_drafts` holds one mutable row per entity name, overwritten on every save.
//! `low_code_entity_versions` is append-only publish history — `rollback` never deletes or
//! rewrites a row, it inserts a new version whose content matches an old one and records
//! `restored_from_version`, same "never modify the past" instinct as the `workflow_events`
//! audit log.

use chrono::{DateTime, Utc};
use metap_metadata::MetadataRegistry;
use sqlx::types::Json;
use sqlx::{PgPool, Row};

use crate::definition::LowCodeEntityDefinition;
use crate::error::PublishError;

#[derive(Debug, Clone)]
pub struct PublishedVersion {
    pub version_number: i32,
    pub definition: LowCodeEntityDefinition,
    pub published_at: DateTime<Utc>,
    pub restored_from_version: Option<i32>,
}

#[derive(Debug, Clone)]
pub struct VersionSummary {
    pub version_number: i32,
    pub published_at: DateTime<Utc>,
    pub restored_from_version: Option<i32>,
}

#[derive(Debug, Clone, Copy)]
pub struct PublishOutcome {
    pub version_number: i32,
}

pub async fn save_draft(
    pool: &PgPool,
    entity_name: &str,
    definition: &LowCodeEntityDefinition,
) -> Result<(), PublishError> {
    if definition.name != entity_name {
        return Err(PublishError::Invalid(metap_metadata::MetadataValidationError {
            entity: entity_name.to_string(),
            issues: vec![format!(
                "definition.name \"{}\" does not match entity name \"{}\" in the URL",
                definition.name, entity_name
            )],
        }));
    }
    definition.validate_shape().map_err(PublishError::Invalid)?;

    sqlx::query(
        "INSERT INTO low_code_entity_drafts (entity_name, definition, updated_at) \
         VALUES ($1, $2, now()) \
         ON CONFLICT (entity_name) DO UPDATE SET definition = $2, updated_at = now()",
    )
    .bind(entity_name)
    .bind(Json(definition))
    .execute(pool)
    .await
    .map_err(PublishError::from)?;
    Ok(())
}

/// Every entity name that currently has a draft (published or not) — used by the admin API's
/// `GET /admin/lowcode/entities` to list what exists for a future builder UI, alongside
/// `list_all_published`.
pub async fn list_draft_names(pool: &PgPool) -> anyhow::Result<Vec<String>> {
    let rows = sqlx::query("SELECT entity_name FROM low_code_entity_drafts ORDER BY entity_name")
        .fetch_all(pool)
        .await?;
    rows.iter().map(|row| Ok(row.try_get("entity_name")?)).collect()
}

pub async fn get_draft(pool: &PgPool, entity_name: &str) -> anyhow::Result<Option<LowCodeEntityDefinition>> {
    let row = sqlx::query("SELECT definition FROM low_code_entity_drafts WHERE entity_name = $1")
        .bind(entity_name)
        .fetch_optional(pool)
        .await?;
    Ok(row.map(|r| r.try_get::<Json<LowCodeEntityDefinition>, _>("definition")).transpose()?.map(|Json(v)| v))
}

pub async fn get_published(
    pool: &PgPool,
    entity_name: &str,
) -> anyhow::Result<Option<PublishedVersion>> {
    let row = sqlx::query(
        "SELECT definition, version_number, published_at, restored_from_version \
         FROM low_code_entity_versions WHERE entity_name = $1 \
         ORDER BY version_number DESC LIMIT 1",
    )
    .bind(entity_name)
    .fetch_optional(pool)
    .await?;
    row.map(version_from_row).transpose()
}

pub async fn list_versions(pool: &PgPool, entity_name: &str) -> anyhow::Result<Vec<VersionSummary>> {
    let rows = sqlx::query(
        "SELECT version_number, published_at, restored_from_version \
         FROM low_code_entity_versions WHERE entity_name = $1 \
         ORDER BY version_number DESC",
    )
    .bind(entity_name)
    .fetch_all(pool)
    .await?;
    rows.iter()
        .map(|row| {
            Ok(VersionSummary {
                version_number: row.try_get("version_number")?,
                published_at: row.try_get("published_at")?,
                restored_from_version: row.try_get("restored_from_version")?,
            })
        })
        .collect()
}

/// Every entity that has ever been published, at its latest version — the merge input
/// `apps/crm-server`'s boot/reload path needs to build a runtime `MetadataRegistry` covering
/// every DB-authored entity, not just one looked up by name.
pub async fn list_all_published(pool: &PgPool) -> anyhow::Result<Vec<(String, LowCodeEntityDefinition)>> {
    let rows = sqlx::query(
        "SELECT DISTINCT ON (entity_name) entity_name, definition \
         FROM low_code_entity_versions \
         ORDER BY entity_name, version_number DESC",
    )
    .fetch_all(pool)
    .await?;
    rows.iter()
        .map(|row| {
            let name: String = row.try_get("entity_name")?;
            let Json(definition) = row.try_get::<Json<LowCodeEntityDefinition>, _>("definition")?;
            Ok((name, definition))
        })
        .collect()
}

fn version_from_row(row: sqlx::postgres::PgRow) -> anyhow::Result<PublishedVersion> {
    let Json(definition) = row.try_get::<Json<LowCodeEntityDefinition>, _>("definition")?;
    Ok(PublishedVersion {
        version_number: row.try_get("version_number")?,
        definition,
        published_at: row.try_get("published_at")?,
        restored_from_version: row.try_get("restored_from_version")?,
    })
}

async fn next_version_number(pool: &PgPool, entity_name: &str) -> anyhow::Result<i32> {
    let max: Option<i32> =
        sqlx::query_scalar("SELECT MAX(version_number) FROM low_code_entity_versions WHERE entity_name = $1")
            .bind(entity_name)
            .fetch_one(pool)
            .await?;
    Ok(max.unwrap_or(0) + 1)
}

/// Builds the registry `publish`/`rollback` validate a candidate definition against: the
/// code-authored `base_registry`, plus every *other* currently-published DB-authored entity
/// (fetched fresh from the DB, not from a possibly-stale in-memory snapshot), plus the
/// candidate itself. Rejects up front if `entity_name` is already taken by a code-authored
/// entity — the check the original TS-era spec deferred for lack of registry access
/// (`docs/low-code-metadata-storage-design.md`), now possible since this crate depends on
/// `metap-metadata`.
async fn build_check_registry(
    pool: &PgPool,
    entity_name: &str,
    candidate: &LowCodeEntityDefinition,
    base_registry: &MetadataRegistry,
) -> Result<MetadataRegistry, PublishError> {
    if base_registry.get_entity(entity_name).is_some() {
        return Err(PublishError::NameReservedByCodeEntity);
    }
    let other_published = list_all_published(pool).await.map_err(PublishError::from)?;
    let mut extra: Vec<_> = other_published
        .into_iter()
        .filter(|(name, _)| name != entity_name)
        .map(|(_, def)| def.to_entity_definition())
        .collect();
    extra.push(candidate.to_entity_definition());
    base_registry.merge_with(extra).map_err(PublishError::from)
}

pub async fn publish(
    pool: &PgPool,
    entity_name: &str,
    base_registry: &MetadataRegistry,
) -> Result<PublishOutcome, PublishError> {
    let draft = get_draft(pool, entity_name).await.map_err(PublishError::from)?;
    let Some(draft) = draft else { return Err(PublishError::NoDraft) };
    draft.validate_shape().map_err(PublishError::Invalid)?;

    let check_registry = build_check_registry(pool, entity_name, &draft, base_registry).await?;
    check_registry.validate_references().map_err(PublishError::Invalid)?;

    let version_number = next_version_number(pool, entity_name).await.map_err(PublishError::from)?;
    sqlx::query(
        "INSERT INTO low_code_entity_versions \
         (entity_name, definition, version_number, restored_from_version) \
         VALUES ($1, $2, $3, NULL)",
    )
    .bind(entity_name)
    .bind(Json(&draft))
    .bind(version_number)
    .execute(pool)
    .await
    .map_err(PublishError::from)?;

    Ok(PublishOutcome { version_number })
}

pub async fn rollback(
    pool: &PgPool,
    entity_name: &str,
    to_version_number: i32,
    base_registry: &MetadataRegistry,
) -> Result<PublishOutcome, PublishError> {
    let row = sqlx::query("SELECT definition FROM low_code_entity_versions WHERE entity_name = $1 AND version_number = $2")
        .bind(entity_name)
        .bind(to_version_number)
        .fetch_optional(pool)
        .await
        .map_err(PublishError::from)?;
    let Some(row) = row else { return Err(PublishError::VersionNotFound(to_version_number)) };
    let Json(target_definition) =
        row.try_get::<Json<LowCodeEntityDefinition>, _>("definition").map_err(PublishError::from)?;

    let check_registry = build_check_registry(pool, entity_name, &target_definition, base_registry).await?;
    check_registry.validate_references().map_err(PublishError::Invalid)?;

    save_draft(pool, entity_name, &target_definition).await?;

    let version_number = next_version_number(pool, entity_name).await.map_err(PublishError::from)?;
    sqlx::query(
        "INSERT INTO low_code_entity_versions \
         (entity_name, definition, version_number, restored_from_version) \
         VALUES ($1, $2, $3, $4)",
    )
    .bind(entity_name)
    .bind(Json(&target_definition))
    .bind(version_number)
    .bind(to_version_number)
    .execute(pool)
    .await
    .map_err(PublishError::from)?;

    Ok(PublishOutcome { version_number })
}
