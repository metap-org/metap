use serde_json::Value;
use sqlx::PgPool;
use uuid::Uuid;

use crate::entry::{AuditEntry, AuditTrailEntryRow};
use crate::store::AuditTrailStore;

/// Same cap `metap_query::plan_list` enforces per entity list view and
/// `list_recent_audit_events` (`../metap-lowcode`) enforces on its own feed — a record with an
/// unusually long edit history must not turn one HTTP response into an unbounded payload.
const MAX_ENTRIES_PER_RECORD: i64 = 200;

/// The default `AuditTrailStore` — one shared table (`metadata.audit_trail_entries`, see
/// `crates/migrations/0032_audit_trail_entries.sql`), matching this codebase's own convention for
/// every other framework-level append-only log (`workflow_events`, `outbox_events`,
/// `low_code_metadata_audit_events` are all single shared tables too — table-per-entity is
/// reserved for `metap-reconciler`-managed business-entity data, a different concept).
///
/// `pool` is whatever `PgPool` a deployment passes in at construction — the tenant's own shared
/// pool by default, or a wholly separate pool pointed at a different Postgres instance entirely,
/// with no code change anywhere else needed either way (see `AuditTrailStore`'s own doc comment
/// for why this impl owns its pool rather than borrowing the caller's transaction).
pub struct PostgresAuditTrailStore {
    pool: PgPool,
}

impl PostgresAuditTrailStore {
    pub fn new(pool: PgPool) -> Self {
        Self { pool }
    }
}

#[async_trait::async_trait]
impl AuditTrailStore for PostgresAuditTrailStore {
    async fn record(&self, tenant_id: Uuid, entry: AuditEntry) -> anyhow::Result<()> {
        anyhow::ensure!(
            tenant_id == entry.tenant_id,
            "AuditTrailStore::record called with tenant_id {tenant_id} but entry.tenant_id is {}",
            entry.tenant_id
        );
        sqlx::query(
            "INSERT INTO metadata.audit_trail_entries \
             (tenant_id, entity, record_id, action, transition_action, actor_user_id, reason, diff, version_after, occurred_at) \
             VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)",
        )
        .bind(entry.tenant_id)
        .bind(&entry.entity)
        .bind(entry.record_id)
        .bind(entry.action.as_str())
        .bind(&entry.transition_action)
        .bind(entry.actor_user_id)
        .bind(&entry.reason)
        .bind(Value::Object(entry.diff))
        .bind(entry.version_after)
        .bind(entry.occurred_at)
        .execute(&self.pool)
        .await?;
        Ok(())
    }

    async fn list_for_record(
        &self,
        tenant_id: Uuid,
        entity: &str,
        record_id: Uuid,
    ) -> anyhow::Result<Vec<AuditTrailEntryRow>> {
        let rows = sqlx::query_as::<_, AuditTrailEntryRow>(
            "SELECT id, tenant_id, entity, record_id, action, transition_action, actor_user_id, \
             reason, diff, version_after, occurred_at \
             FROM metadata.audit_trail_entries \
             WHERE tenant_id = $1 AND entity = $2 AND record_id = $3 \
             ORDER BY occurred_at DESC LIMIT $4",
        )
        .bind(tenant_id)
        .bind(entity)
        .bind(record_id)
        .bind(MAX_ENTRIES_PER_RECORD)
        .fetch_all(&self.pool)
        .await?;
        Ok(rows)
    }
}
