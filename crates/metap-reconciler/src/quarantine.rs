//! §4.5 — a per-entity table holding rows a migration couldn't transform, moved out *before*
//! the batch transform runs (so the transform never hits them and never fails mid-batch).
//! `original_data` is kept verbatim for a human to inspect/fix later (`resolve`).

use serde_json::Value;
use sqlx::PgPool;
use uuid::Uuid;

use crate::sqlfmt::quote_qualified_ident;

#[derive(Debug, Clone, Copy, Default, PartialEq, Eq)]
pub struct QuarantineOutcome {
    pub quarantined: i64,
    /// A candidate row that also has an incoming `ON DELETE RESTRICT` FK from another entity's
    /// table couldn't be deleted out of the main table — Postgres itself refused it
    /// (`foreign_key_violation`), which is exactly §4.5's "không quarantine row đang bị ref
    /// (tránh orphan mới)" rule, enforced by the database rather than by this crate tracking
    /// the cross-entity FK graph itself. Left in place, still "bad", for the caller to see via
    /// `bad_rows - quarantined` after a subsequent `preflight`.
    pub skipped_referenced: i64,
}

pub fn quarantine_table_name(table: &str) -> String {
    format!("{table}_quarantine")
}

pub async fn ensure_quarantine_table(pool: &PgPool, table: &str) -> anyhow::Result<()> {
    let name = quote_qualified_ident(&quarantine_table_name(table));
    let sql = format!(
        "CREATE TABLE IF NOT EXISTS {name} (\
         id uuid PRIMARY KEY, \
         tenant_id uuid NOT NULL, \
         original_data jsonb NOT NULL, \
         migration_id text NOT NULL, \
         reason text NOT NULL, \
         detail jsonb, \
         quarantined_at timestamptz NOT NULL DEFAULT now(), \
         resolved_at timestamptz)"
    );
    sqlx::query(&sql).execute(pool).await?;
    Ok(())
}

/// `bad_predicate` is a raw SQL boolean expression over the main table (aliased `t`) selecting
/// which rows to move out — callers pass the *same* predicate `migration::preflight` used to
/// count them, so "how many are bad" and "which ones get quarantined" can never drift apart.
///
/// Scoped to `tenant_id` throughout (found live, `AUDIT_2.md`: the candidate `SELECT` had no
/// tenant filter at all, relying purely on the "one dedicated table per `DedicatedDb` tenant"
/// convention) — makes that convention structurally true for this operation rather than only
/// documented.
pub async fn quarantine_bad_rows(
    pool: &PgPool,
    tenant_id: Uuid,
    table: &str,
    bad_predicate: &str,
    migration_id: &str,
    reason: &str,
) -> anyhow::Result<QuarantineOutcome> {
    ensure_quarantine_table(pool, table).await?;
    let quoted_table = quote_qualified_ident(table);
    let quoted_quarantine = quote_qualified_ident(&quarantine_table_name(table));

    let candidate_ids: Vec<Uuid> = sqlx::query_scalar(&format!(
        "SELECT t.id FROM {quoted_table} t WHERE t.tenant_id = $1 AND ({bad_predicate})"
    ))
    .bind(tenant_id)
    .fetch_all(pool)
    .await?;

    let mut outcome = QuarantineOutcome::default();
    for id in candidate_ids {
        let mut tx = pool.begin().await?;
        let fetched: Option<(Uuid, Value)> = sqlx::query_as(&format!(
            "SELECT tenant_id, data FROM {quoted_table} WHERE id = $1 AND tenant_id = $2"
        ))
        .bind(id)
        .bind(tenant_id)
        .fetch_optional(&mut *tx)
        .await?;
        let Some((tenant_id, data)) = fetched else {
            tx.rollback().await?;
            continue; // already gone (concurrent write) — nothing to quarantine
        };

        let delete_result = sqlx::query(&format!("DELETE FROM {quoted_table} WHERE id = $1 AND tenant_id = $2"))
            .bind(id)
            .bind(tenant_id)
            .execute(&mut *tx)
            .await;

        match delete_result {
            Ok(_) => {
                sqlx::query(&format!(
                    "INSERT INTO {quoted_quarantine} (id, tenant_id, original_data, migration_id, reason) \
                     VALUES ($1, $2, $3, $4, $5)"
                ))
                .bind(id)
                .bind(tenant_id)
                .bind(&data)
                .bind(migration_id)
                .bind(reason)
                .execute(&mut *tx)
                .await?;
                tx.commit().await?;
                outcome.quarantined += 1;
            }
            Err(err) => {
                tx.rollback().await?;
                if is_foreign_key_violation(&err) {
                    outcome.skipped_referenced += 1;
                } else {
                    return Err(err.into());
                }
            }
        }
    }
    Ok(outcome)
}

fn is_foreign_key_violation(err: &sqlx::Error) -> bool {
    err.as_database_error().and_then(|d| d.code()).as_deref() == Some("23503")
}

/// A human (or a support tool) has fixed `corrected_data` outside this crate — re-inserts the
/// row into the main table at its original `id`/`tenant_id` and marks the quarantine row
/// resolved. **Simplification**: this does not "replay the full transform chain" the design
/// doc's §4.5 describes (`Resolve đưa row về bảng chính ở version HIỆN TẠI, áp full transform
/// chain`) — this crate has no persistent, queryable log of which `MigrationOp`s have been
/// applied historically to replay, so resolving means the corrected data must already be valid
/// for the table's *current* schema; the caller is responsible for that, not this function.
pub async fn resolve(pool: &PgPool, table: &str, quarantine_id: Uuid, corrected_data: Value) -> anyhow::Result<()> {
    let quoted_table = quote_qualified_ident(table);
    let quoted_quarantine = quote_qualified_ident(&quarantine_table_name(table));
    let mut tx = pool.begin().await?;

    let tenant_id: Option<Uuid> = sqlx::query_scalar(&format!(
        "SELECT tenant_id FROM {quoted_quarantine} WHERE id = $1 AND resolved_at IS NULL"
    ))
    .bind(quarantine_id)
    .fetch_optional(&mut *tx)
    .await?;
    let Some(tenant_id) = tenant_id else {
        anyhow::bail!("quarantine row {quarantine_id} not found or already resolved");
    };

    sqlx::query(&format!(
        "INSERT INTO {quoted_table} (id, tenant_id, data) VALUES ($1, $2, $3)"
    ))
    .bind(quarantine_id)
    .bind(tenant_id)
    .bind(&corrected_data)
    .execute(&mut *tx)
    .await?;
    sqlx::query(&format!(
        "UPDATE {quoted_quarantine} SET resolved_at = now() WHERE id = $1"
    ))
    .bind(quarantine_id)
    .execute(&mut *tx)
    .await?;

    tx.commit().await?;
    Ok(())
}
