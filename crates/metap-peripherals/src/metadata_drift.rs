//! Mirrors `packages/core/src/core/metadata/metadata-drift.ts`. Never lets a DB hiccup at
//! boot become a crash — mirrors `HealthService`'s graceful-degradation stance elsewhere in
//! this codebase. Drift detection is best-effort visibility, not a startup precondition, so
//! this returns nothing and cannot fail: every error path is caught and logged internally,
//! exactly like the TS version's outer `try`/`catch`.

use metap_metadata::EntitySummary;
use sqlx::PgPool;

pub async fn check(pool: &PgPool, entities: &[EntitySummary]) {
    if let Err(err) = check_inner(pool, entities).await {
        eprintln!("metadata: drift check skipped, could not reach the database: {err:#}");
    }
}

async fn check_inner(pool: &PgPool, entities: &[EntitySummary]) -> anyhow::Result<()> {
    for entity in entities {
        let existing: Option<String> =
            sqlx::query_scalar("SELECT hash FROM metadata_versions WHERE entity_name = $1")
                .bind(&entity.name)
                .fetch_optional(pool)
                .await?;

        match &existing {
            None => {
                eprintln!(
                    "metadata: first boot, recording initial hash (entity={}, hash={})",
                    entity.name, entity.version
                );
            }
            Some(hash) if hash != &entity.version => {
                eprintln!(
                    "metadata: drift detected since last boot (entity={}, oldHash={}, newHash={})",
                    entity.name, hash, entity.version
                );
            }
            Some(_) => {}
        }

        sqlx::query(
            "INSERT INTO metadata_versions (entity_name, hash) VALUES ($1, $2) \
             ON CONFLICT (entity_name) DO UPDATE SET hash = $2, updated_at = now()",
        )
        .bind(&entity.name)
        .bind(&entity.version)
        .execute(pool)
        .await?;
    }
    Ok(())
}
