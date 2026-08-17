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

#[derive(Debug, Clone)]
pub struct PublishOutcome {
    pub version_number: i32,
    /// The already-validated merged registry (`base_registry` + every other currently
    /// published DB-authored entity + this publish/rollback's own definition) — the caller
    /// (`metap-lowcode-http`) swaps this straight into its live `ArcSwap` instead of
    /// re-querying and re-merging from scratch, which used to mean every publish/rollback
    /// paid for the same `list_all_published` + registry build twice.
    pub registry: MetadataRegistry,
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

/// Every entity name that currently has a draft (published or not), with its current
/// enabled/disabled flag — used by the admin API's `GET /admin/lowcode/entities` to list what
/// exists and drive the enabled toggle, alongside `list_all_published`.
pub async fn list_draft_statuses(pool: &PgPool) -> anyhow::Result<Vec<(String, bool)>> {
    let rows = sqlx::query("SELECT entity_name, enabled FROM low_code_entity_drafts ORDER BY entity_name")
        .fetch_all(pool)
        .await?;
    rows.iter()
        .map(|row| Ok((row.try_get("entity_name")?, row.try_get("enabled")?)))
        .collect()
}

/// Flips a published entity's enabled flag — disabled entities are excluded from
/// `list_enabled_published` and therefore from the runtime-serving `MetadataRegistry` (no
/// restart needed, same hot-reload mechanism as publish/rollback: the caller in
/// `metap-lowcode-http` rebuilds and swaps the live registry after this returns) without
/// touching the entity's draft/version history — re-enabling it brings back exactly what was
/// there before. A no-op, not an error, if `entity_name` has no draft row (nothing to flip).
pub async fn set_enabled(pool: &PgPool, entity_name: &str, enabled: bool) -> anyhow::Result<()> {
    sqlx::query("UPDATE low_code_entity_drafts SET enabled = $2 WHERE entity_name = $1")
        .bind(entity_name)
        .bind(enabled)
        .execute(pool)
        .await?;
    Ok(())
}

pub async fn get_draft(pool: &PgPool, entity_name: &str) -> anyhow::Result<Option<LowCodeEntityDefinition>> {
    let row = sqlx::query("SELECT definition FROM low_code_entity_drafts WHERE entity_name = $1")
        .bind(entity_name)
        .fetch_optional(pool)
        .await?;
    Ok(row
        .map(|r| r.try_get::<Json<LowCodeEntityDefinition>, _>("definition"))
        .transpose()?
        .map(|Json(v)| v))
}

pub async fn get_published(pool: &PgPool, entity_name: &str) -> anyhow::Result<Option<PublishedVersion>> {
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

/// Same as `list_all_published` but excludes disabled entities — this is the one the
/// runtime-serving `MetadataRegistry` (boot merge in `apps/crm-server`, and every reload
/// after a publish/rollback/enable-toggle in `metap-lowcode-http`) is actually built from,
/// and the one `build_check_registry` validates a new publish/rollback against. A published
/// entity that references a *disabled* one (via a `reference` field's `refEntity`) will fail
/// that validation as if the referenced entity didn't exist — a known, deliberate tradeoff:
/// disabling isn't meant to be transparent to entities that depend on the disabled one.
pub async fn list_enabled_published(pool: &PgPool) -> anyhow::Result<Vec<(String, LowCodeEntityDefinition)>> {
    let rows = sqlx::query(
        "SELECT DISTINCT ON (v.entity_name) v.entity_name, v.definition \
         FROM low_code_entity_versions v \
         JOIN low_code_entity_drafts d ON d.entity_name = v.entity_name \
         WHERE d.enabled = true \
         ORDER BY v.entity_name, v.version_number DESC",
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

/// Computes the next version number and inserts the row in one transaction, guarded by a
/// Postgres advisory lock scoped to `entity_name` (`pg_advisory_xact_lock`, auto-released on
/// commit/rollback) — without it, two concurrent `publish`/`rollback` calls for the same
/// entity could both read the same `MAX(version_number)` and both try to insert it, tripping
/// the `(entity_name, version_number)` unique constraint and surfacing as an opaque 500
/// instead of serializing cleanly. `hashtext` turns the name into the `bigint` key
/// `pg_advisory_xact_lock` wants; a hash collision between two different entity names would
/// only over-serialize (briefly block an unrelated publish), never under-serialize, so it's
/// safe even though `hashtext` isn't collision-free.
async fn insert_version(
    pool: &PgPool,
    entity_name: &str,
    definition: &LowCodeEntityDefinition,
    restored_from_version: Option<i32>,
) -> anyhow::Result<i32> {
    let mut tx = pool.begin().await?;
    sqlx::query("SELECT pg_advisory_xact_lock(hashtext($1))")
        .bind(entity_name)
        .execute(&mut *tx)
        .await?;
    let max: Option<i32> =
        sqlx::query_scalar("SELECT MAX(version_number) FROM low_code_entity_versions WHERE entity_name = $1")
            .bind(entity_name)
            .fetch_one(&mut *tx)
            .await?;
    let version_number = max.unwrap_or(0) + 1;
    sqlx::query(
        "INSERT INTO low_code_entity_versions \
         (entity_name, definition, version_number, restored_from_version) \
         VALUES ($1, $2, $3, $4)",
    )
    .bind(entity_name)
    .bind(Json(definition))
    .bind(version_number)
    .bind(restored_from_version)
    .execute(&mut *tx)
    .await?;
    tx.commit().await?;
    Ok(version_number)
}

struct CheckRegistry {
    /// `base_registry` + every *other* currently-enabled-published DB-authored entity +
    /// `candidate` — used for `validate_references()`, which needs the candidate present to
    /// check its own outbound `refEntity`s.
    registry: MetadataRegistry,
    /// The same "other enabled entities" defs `registry` was merged from, *without*
    /// `candidate` — kept around so `publish`/`rollback` can rebuild a registry that omits
    /// `candidate` (when `entity_name` is disabled) without a second DB round-trip.
    other_published: Vec<metap_metadata::EntityDefinition>,
}

/// Builds the registry `publish`/`rollback` validate a candidate definition against: the
/// code-authored `base_registry`, plus every *other* currently-enabled-published DB-authored
/// entity (fetched fresh from the DB, not from a possibly-stale in-memory snapshot), plus the
/// candidate itself. Rejects up front if `entity_name` is already taken by a code-authored
/// entity — the check the original TS-era spec deferred for lack of registry access
/// (`docs/low-code-metadata-storage-design.md`), now possible since this crate depends on
/// `metap-metadata`.
async fn build_check_registry(
    pool: &PgPool,
    entity_name: &str,
    candidate: &LowCodeEntityDefinition,
    base_registry: &MetadataRegistry,
) -> Result<CheckRegistry, PublishError> {
    if base_registry.get_entity(entity_name).is_some() {
        return Err(PublishError::NameReservedByCodeEntity);
    }
    let published = list_enabled_published(pool).await.map_err(PublishError::from)?;
    let other_published: Vec<_> = published
        .into_iter()
        .filter(|(name, _)| name != entity_name)
        .map(|(_, def)| def.to_entity_definition())
        .collect();
    let mut extra = other_published.clone();
    extra.push(candidate.to_entity_definition());
    let registry = base_registry.merge_with(extra).map_err(PublishError::from)?;
    Ok(CheckRegistry {
        registry,
        other_published,
    })
}

/// The registry `publish`/`rollback` actually swap live: `check.registry` (candidate
/// included) if `entity_name` is currently enabled, otherwise `check.other_published` merged
/// *without* the candidate — publishing/rolling back a disabled entity's definition must not
/// implicitly re-enable it (the enabled flag is independent of publish history; only an
/// explicit `set_enabled` call flips it).
async fn live_registry_for(
    pool: &PgPool,
    entity_name: &str,
    base_registry: &MetadataRegistry,
    check: CheckRegistry,
) -> Result<MetadataRegistry, PublishError> {
    let enabled: Option<bool> = sqlx::query_scalar("SELECT enabled FROM low_code_entity_drafts WHERE entity_name = $1")
        .bind(entity_name)
        .fetch_optional(pool)
        .await
        .map_err(PublishError::from)?;
    if enabled.unwrap_or(true) {
        Ok(check.registry)
    } else {
        base_registry
            .merge_with(check.other_published)
            .map_err(PublishError::from)
    }
}

/// The read-only half of `publish`: everything that can reject a draft (missing draft, shape
/// validation, name-reservation, cross-reference validation) with no side effect — shared by
/// `publish` itself and `preview_publish` (`docs/roadmap.md` Phase 11 Phase B's publish
/// preview/validation report), which needs the exact same checks without the write.
async fn validate_for_publish(
    pool: &PgPool,
    entity_name: &str,
    base_registry: &MetadataRegistry,
) -> Result<(LowCodeEntityDefinition, CheckRegistry), PublishError> {
    let draft = get_draft(pool, entity_name).await.map_err(PublishError::from)?;
    let Some(draft) = draft else {
        return Err(PublishError::NoDraft);
    };
    draft.validate_shape().map_err(PublishError::Invalid)?;

    let check = build_check_registry(pool, entity_name, &draft, base_registry).await?;
    check.registry.validate_references().map_err(PublishError::Invalid)?;

    Ok((draft, check))
}

pub async fn publish(
    pool: &PgPool,
    entity_name: &str,
    base_registry: &MetadataRegistry,
) -> Result<PublishOutcome, PublishError> {
    let (draft, check) = validate_for_publish(pool, entity_name, base_registry).await?;

    let version_number = insert_version(pool, entity_name, &draft, None)
        .await
        .map_err(PublishError::from)?;
    let registry = live_registry_for(pool, entity_name, base_registry, check).await?;

    Ok(PublishOutcome {
        version_number,
        registry,
    })
}

#[derive(Debug, Clone)]
pub struct PublishPreview {
    /// The version number publishing right now would produce — advisory only. `insert_version`
    /// computes the real one inside an advisory-locked transaction (see its doc comment on the
    /// concurrent-publish race it guards against); this is a plain unlocked read, so it can go
    /// stale if another publish for the same entity lands between this preview and a real
    /// publish. Good enough for "here's roughly what you're about to do," not a reservation.
    pub would_be_version: i32,
}

/// Runs every check `publish` would, without writing anything — no new `low_code_entity_versions`
/// row, no live registry swap. Lets an operator validate a draft before committing to a new
/// published version (`docs/roadmap.md` Phase 11 Phase B).
pub async fn preview_publish(
    pool: &PgPool,
    entity_name: &str,
    base_registry: &MetadataRegistry,
) -> Result<PublishPreview, PublishError> {
    validate_for_publish(pool, entity_name, base_registry).await?;
    let max: Option<i32> =
        sqlx::query_scalar("SELECT MAX(version_number) FROM low_code_entity_versions WHERE entity_name = $1")
            .bind(entity_name)
            .fetch_one(pool)
            .await
            .map_err(PublishError::from)?;
    Ok(PublishPreview {
        would_be_version: max.unwrap_or(0) + 1,
    })
}

pub async fn rollback(
    pool: &PgPool,
    entity_name: &str,
    to_version_number: i32,
    base_registry: &MetadataRegistry,
) -> Result<PublishOutcome, PublishError> {
    let row =
        sqlx::query("SELECT definition FROM low_code_entity_versions WHERE entity_name = $1 AND version_number = $2")
            .bind(entity_name)
            .bind(to_version_number)
            .fetch_optional(pool)
            .await
            .map_err(PublishError::from)?;
    let Some(row) = row else {
        return Err(PublishError::VersionNotFound(to_version_number));
    };
    let Json(target_definition) = row
        .try_get::<Json<LowCodeEntityDefinition>, _>("definition")
        .map_err(PublishError::from)?;

    let check = build_check_registry(pool, entity_name, &target_definition, base_registry).await?;
    check.registry.validate_references().map_err(PublishError::Invalid)?;

    save_draft(pool, entity_name, &target_definition).await?;

    let version_number = insert_version(pool, entity_name, &target_definition, Some(to_version_number))
        .await
        .map_err(PublishError::from)?;
    let registry = live_registry_for(pool, entity_name, base_registry, check).await?;

    Ok(PublishOutcome {
        version_number,
        registry,
    })
}
