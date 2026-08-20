//! Append-only audit trail for who/when changed a DB-authored entity's draft/publish/enabled
//! state (`docs/roadmap.md` Phase 11 Phase C: "audit log cho metadata" — the first Phase C
//! deliverable, `docs/low-code-platform-v1.md`). Deliberately a separate table/module from
//! `store.rs`'s `low_code_entity_versions` (which is itself already an append-only history of
//! *content*) — this table answers "who did it and when", not "what did it produce"; the two
//! are cross-referenced only loosely, by `entity_name` + `version_number`.
//!
//! **Not wired into `store.rs`'s transactions on purpose.** `save_draft`/`set_enabled`/
//! `publish`/`rollback` already have ~40 direct call sites across
//! `crates/metap-lowcode/tests/store.rs`; threading an actor parameter through every one of
//! them for a governance/observability feature would be a large, low-value mechanical diff.
//! Instead `metap-lowcode-http`'s handlers — which already hold the caller's `RequestContext`
//! and already call `store.rs`'s functions — call [`record`] once immediately after a
//! successful store call. This means an audit event can in principle be lost if the process
//! crashes between the store write committing and this insert running; acceptable for
//! "operational visibility", not meant to be a tamper-evident compliance log.

use chrono::{DateTime, Utc};
use sqlx::PgPool;

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum AuditAction {
    DraftSaved,
    Published,
    RolledBack,
    Enabled,
    Disabled,
}

impl AuditAction {
    fn as_str(self) -> &'static str {
        match self {
            AuditAction::DraftSaved => "draft_saved",
            AuditAction::Published => "published",
            AuditAction::RolledBack => "rolled_back",
            AuditAction::Enabled => "enabled",
            AuditAction::Disabled => "disabled",
        }
    }
}

/// Who performed the action — mirrors the two fields of `metap_permission::RequestContext`
/// this module actually needs, so `metap-lowcode` doesn't have to depend on `metap-http`/
/// `metap-permission` just to accept a whole `RequestContext`.
#[derive(Debug, Clone)]
pub struct AuditActor {
    pub user_id: Option<String>,
    pub tenant_id: String,
}

#[derive(Debug, Clone)]
pub struct AuditEvent {
    pub action: String,
    pub actor_user_id: Option<String>,
    pub actor_tenant_id: String,
    pub version_number: Option<i32>,
    pub restored_from_version: Option<i32>,
    pub occurred_at: DateTime<Utc>,
}

#[derive(Default, Clone, Copy)]
pub struct AuditVersionInfo {
    pub version_number: Option<i32>,
    pub restored_from_version: Option<i32>,
}

/// Best-effort — logs and swallows a DB error instead of propagating, so a write to the audit
/// table can never turn an otherwise-successful draft/publish/rollback/enable-toggle into a
/// failed HTTP response. See the module doc comment for why this isn't inside the same
/// transaction as the write it's recording.
pub async fn record(pool: &PgPool, entity_name: &str, action: AuditAction, actor: &AuditActor, info: AuditVersionInfo) {
    let result = sqlx::query(
        "INSERT INTO low_code_metadata_audit_events \
         (entity_name, action, actor_user_id, actor_tenant_id, version_number, restored_from_version) \
         VALUES ($1, $2, $3, $4, $5, $6)",
    )
    .bind(entity_name)
    .bind(action.as_str())
    .bind(&actor.user_id)
    .bind(&actor.tenant_id)
    .bind(info.version_number)
    .bind(info.restored_from_version)
    .execute(pool)
    .await;
    if let Err(e) = result {
        tracing::warn!(entity_name, action = action.as_str(), error = %e, "failed to record low-code metadata audit event");
    }
}

pub async fn list_for_entity(pool: &PgPool, entity_name: &str) -> anyhow::Result<Vec<AuditEvent>> {
    let rows = sqlx::query_as::<_, (String, Option<String>, String, Option<i32>, Option<i32>, DateTime<Utc>)>(
        "SELECT action, actor_user_id, actor_tenant_id, version_number, restored_from_version, occurred_at \
         FROM low_code_metadata_audit_events WHERE entity_name = $1 ORDER BY occurred_at DESC",
    )
    .bind(entity_name)
    .fetch_all(pool)
    .await?;
    Ok(rows
        .into_iter()
        .map(
            |(action, actor_user_id, actor_tenant_id, version_number, restored_from_version, occurred_at)| AuditEvent {
                action,
                actor_user_id,
                actor_tenant_id,
                version_number,
                restored_from_version,
                occurred_at,
            },
        )
        .collect())
}
