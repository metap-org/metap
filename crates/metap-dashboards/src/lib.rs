//! Customizable dashboard layouts (`docs/roadmap/32-jira-jql-search-charts.md`'s "còn lại").
//! A plain ops-config table (`dashboard_configs`), same shape as `metap-cron`/`policies` — no
//! HTTP, no business-entity knowledge, a library any app wires a router on top of.
//!
//! Two levels, resolved by `get_effective_dashboard`: a **tenant default** (`owner_user_id IS
//! NULL`, one per tenant, meant to be admin-write-only — that check belongs at the HTTP layer,
//! this crate has no concept of roles) and a **personal override** (`owner_user_id = <user>`,
//! any authenticated user writes only their own). A user with no personal layout falls back to
//! the tenant default; a tenant with neither has nothing here at all — the caller supplies its
//! own hardcoded starter widget set in that case, not this crate.

use serde::{Deserialize, Serialize};
use sqlx::PgExecutor;
use uuid::Uuid;

#[derive(Debug, Clone, Serialize, Deserialize, sqlx::FromRow)]
pub struct DashboardConfig {
    pub id: Uuid,
    pub tenant_id: Uuid,
    pub owner_user_id: Option<Uuid>,
    pub layout: serde_json::Value,
    pub updated_at: chrono::DateTime<chrono::Utc>,
    pub updated_by: Option<Uuid>,
}

const COLUMNS: &str = "id, tenant_id, owner_user_id, layout, updated_at, updated_by";

pub async fn get_personal<'e>(
    executor: impl PgExecutor<'e>,
    tenant_id: Uuid,
    user_id: Uuid,
) -> anyhow::Result<Option<DashboardConfig>> {
    let row = sqlx::query_as::<_, DashboardConfig>(&format!(
        "SELECT {COLUMNS} FROM dashboard_configs WHERE tenant_id = $1 AND owner_user_id = $2"
    ))
    .bind(tenant_id)
    .bind(user_id)
    .fetch_optional(executor)
    .await?;
    Ok(row)
}

pub async fn get_tenant_default<'e>(
    executor: impl PgExecutor<'e>,
    tenant_id: Uuid,
) -> anyhow::Result<Option<DashboardConfig>> {
    let row = sqlx::query_as::<_, DashboardConfig>(&format!(
        "SELECT {COLUMNS} FROM dashboard_configs WHERE tenant_id = $1 AND owner_user_id IS NULL"
    ))
    .bind(tenant_id)
    .fetch_optional(executor)
    .await?;
    Ok(row)
}

/// The dashboard a request should actually render: the caller's own personal layout if they've
/// saved one, otherwise the tenant's shared default, otherwise `None` (a brand-new tenant with
/// nobody having configured anything yet). Takes a `&mut PgConnection` (not a generic
/// `PgExecutor`, unlike every other function here) because it needs to issue two queries against
/// the same connection — a `Transaction` isn't `Copy`, so the caller reborrows it (`&mut *tx`)
/// the same way it would for two sequential calls anyway.
pub async fn get_effective_dashboard(
    executor: &mut sqlx::PgConnection,
    tenant_id: Uuid,
    user_id: Uuid,
) -> anyhow::Result<Option<DashboardConfig>> {
    if let Some(personal) = get_personal(&mut *executor, tenant_id, user_id).await? {
        return Ok(Some(personal));
    }
    get_tenant_default(executor, tenant_id).await
}

async fn upsert<'e>(
    executor: impl PgExecutor<'e>,
    tenant_id: Uuid,
    owner_user_id: Option<Uuid>,
    layout: serde_json::Value,
    updated_by: Uuid,
) -> anyhow::Result<DashboardConfig> {
    let row = sqlx::query_as::<_, DashboardConfig>(&format!(
        "INSERT INTO dashboard_configs (tenant_id, owner_user_id, layout, updated_by) \
         VALUES ($1, $2, $3, $4) \
         ON CONFLICT (tenant_id, owner_user_id) DO UPDATE \
         SET layout = EXCLUDED.layout, updated_at = now(), updated_by = EXCLUDED.updated_by \
         RETURNING {COLUMNS}"
    ))
    .bind(tenant_id)
    .bind(owner_user_id)
    .bind(layout)
    .bind(updated_by)
    .fetch_one(executor)
    .await?;
    Ok(row)
}

pub async fn upsert_personal<'e>(
    executor: impl PgExecutor<'e>,
    tenant_id: Uuid,
    user_id: Uuid,
    layout: serde_json::Value,
) -> anyhow::Result<DashboardConfig> {
    upsert(executor, tenant_id, Some(user_id), layout, user_id).await
}

/// Caller (the HTTP layer) must have already checked the actor is allowed to edit the tenant
/// default — same posture `PoliciesAdminPage`'s backing routes take (`AdminContext`, not
/// `AuthContext`); this function itself has no notion of roles.
pub async fn upsert_tenant_default<'e>(
    executor: impl PgExecutor<'e>,
    tenant_id: Uuid,
    layout: serde_json::Value,
    updated_by: Uuid,
) -> anyhow::Result<DashboardConfig> {
    upsert(executor, tenant_id, None, layout, updated_by).await
}
