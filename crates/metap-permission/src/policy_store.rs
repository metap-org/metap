//! `PolicyStore` is a trait, not a concrete type, for the same reason `EventBus` is (see
//! `docs/architectures/09-adr.md`): swapping storage without touching
//! `PermissionSnapshot`/`PermissionService`.

use async_trait::async_trait;
use serde::{Deserialize, Serialize};
use sqlx::types::Json;
use sqlx::Row;
use uuid::Uuid;

use crate::policy_condition::PolicyCondition;

/// `Deny` overrides `Allow` (`docs/roadmap.md`'s permission-review findings, 2026-08-21) — if
/// any matching policy on a call is `Deny`, the call is forbidden regardless of how many
/// `Allow` policies also match. Solves the one thing a purely-additive OR-of-allow model
/// structurally can't: excluding one specific role/condition even though some *other*,
/// independently-authored policy would otherwise allow it. Defaults to `Allow` everywhere (DB
/// column default, `parse`'s fallback) so every policy written before this existed keeps
/// meaning exactly what it always meant.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
pub enum PolicyEffect {
    Allow,
    Deny,
}

impl PolicyEffect {
    pub fn as_str(&self) -> &'static str {
        match self {
            PolicyEffect::Allow => "allow",
            PolicyEffect::Deny => "deny",
        }
    }

    /// Anything other than exactly `"deny"` parses as `Allow` — a defensive default matching
    /// the DB column's own default, not a validated enum at the SQL layer.
    pub fn parse(s: &str) -> Self {
        if s == "deny" {
            PolicyEffect::Deny
        } else {
            PolicyEffect::Allow
        }
    }
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct PolicyRow {
    pub id: Uuid,
    pub tenant_id: Uuid,
    pub entity: String,
    pub action: String,
    pub field: Option<String>,
    pub subject: String,
    pub roles: Option<Vec<String>>,
    pub condition: Option<PolicyCondition>,
    pub created_by: Option<Uuid>,
    pub effect: PolicyEffect,
}

/// `pub` (not crate-private) so `metap-control::PostgresPolicyStore` — which implements this
/// module's `PolicyStore` trait but must live in `metap-control` to reach `Router` without a
/// dependency cycle (`metap-metadata -> metap-permission`, `metap-peripherals -> metap-metadata`,
/// `metap-control -> metap-peripherals`; `metap-permission -> metap-control` would close the
/// loop) — can reuse the exact same row-mapping instead of duplicating it.
pub fn row_from_sql(row: &sqlx::postgres::PgRow) -> anyhow::Result<PolicyRow> {
    Ok(PolicyRow {
        id: row.try_get("id")?,
        tenant_id: row.try_get("tenant_id")?,
        entity: row.try_get("entity")?,
        action: row.try_get("action")?,
        field: row.try_get("field")?,
        subject: row.try_get("subject")?,
        roles: row.try_get::<Option<Json<Vec<String>>>, _>("roles")?.map(|Json(v)| v),
        condition: row
            .try_get::<Option<Json<PolicyCondition>>, _>("condition")?
            .map(|Json(v)| v),
        created_by: row.try_get("created_by")?,
        effect: PolicyEffect::parse(&row.try_get::<String, _>("effect")?),
    })
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum PolicySubject {
    Context,
    Record,
}

impl PolicySubject {
    pub fn as_str(&self) -> &'static str {
        match self {
            PolicySubject::Context => "context",
            PolicySubject::Record => "record",
        }
    }
}

#[derive(Debug, Default)]
pub struct ExplainOptions {
    pub field: Option<String>,
    pub subject: Option<PolicySubject>,
}

#[async_trait]
pub trait PolicyStore: Send + Sync {
    async fn find_context_policies(
        &self,
        tenant_id: Uuid,
        entity: &str,
        action: &str,
    ) -> anyhow::Result<Vec<PolicyRow>>;

    async fn load_all_policies(&self, tenant_id: Uuid, entity: &str) -> anyhow::Result<Vec<PolicyRow>>;

    async fn find_explain_policies(
        &self,
        tenant_id: Uuid,
        entity: &str,
        action: &str,
        options: &ExplainOptions,
    ) -> anyhow::Result<Vec<PolicyRow>>;

    async fn list_policies(&self, tenant_id: Uuid, entity: Option<&str>) -> anyhow::Result<Vec<PolicyRow>>;

    #[allow(clippy::too_many_arguments)]
    async fn create_policy(
        &self,
        tenant_id: Uuid,
        entity: &str,
        action: &str,
        roles: Option<Vec<String>>,
        condition: Option<PolicyCondition>,
        created_by: Option<Uuid>,
        field: Option<&str>,
        subject: Option<PolicySubject>,
        effect: PolicyEffect,
    ) -> anyhow::Result<PolicyRow>;

    async fn delete_policy(&self, tenant_id: Uuid, id: Uuid) -> anyhow::Result<()>;
}

// The Postgres/`Router`-backed impl of this trait, `PostgresPolicyStore`, lives in
// `metap_control` instead of here — see `row_from_sql`'s doc comment above for why.
