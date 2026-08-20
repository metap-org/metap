//! Mirrors `packages/core/src/core/auth/role-assignment-service.ts`. `get_roles_for_user`
//! has a second, earlier copy in `crates/metap-http/src/auth.rs`'s `AuthContext` extractor
//! (Migration Order step 8 pulled the read path forward — no route can authenticate
//! without it). This is the canonical version; `metap-http` should be pointed at this one
//! instead of its inline copy the next time that crate is touched, so there's a single
//! implementation, not two that can drift.
//!
//! Every function here is generic over `sqlx::PgExecutor` rather than pinned to `&PgPool`
//! (`docs/roadmap.md` Phase 16 gap, closed 2026-08-20): tenant provisioning
//! (`metap_control::provisioning`) still calls these against a bare pool it just opened
//! (before a `control.tenants` row exists, so `Router` has nothing to route to yet), while
//! `crates/metap-http/src/auth.rs`/`routes/admin.rs` now call them against a
//! `Router::begin(tenant_id)`-opened transaction — same `E: PgExecutor` pattern already used
//! by `metap-crud::crud_service`'s `fetch_existing`. Both caller shapes compile unchanged
//! against the same implementation.

use sqlx::PgExecutor;
use uuid::Uuid;

pub async fn get_roles_for_user<'e>(
    executor: impl PgExecutor<'e>,
    tenant_id: Uuid,
    user_id: Uuid,
) -> anyhow::Result<Vec<String>> {
    let roles: Vec<String> = sqlx::query_scalar("SELECT role FROM user_roles WHERE tenant_id = $1 AND user_id = $2")
        .bind(tenant_id)
        .bind(user_id)
        .fetch_all(executor)
        .await?;
    Ok(roles)
}

pub async fn assign_role<'e>(
    executor: impl PgExecutor<'e>,
    tenant_id: Uuid,
    user_id: Uuid,
    role: &str,
    assigned_by: Option<Uuid>,
) -> anyhow::Result<()> {
    sqlx::query(
        "INSERT INTO user_roles (tenant_id, user_id, role, created_by) VALUES ($1, $2, $3, $4) \
         ON CONFLICT (tenant_id, user_id, role) DO NOTHING",
    )
    .bind(tenant_id)
    .bind(user_id)
    .bind(role)
    .bind(assigned_by)
    .execute(executor)
    .await?;
    Ok(())
}

pub async fn revoke_role<'e>(
    executor: impl PgExecutor<'e>,
    tenant_id: Uuid,
    user_id: Uuid,
    role: &str,
) -> anyhow::Result<()> {
    sqlx::query("DELETE FROM user_roles WHERE tenant_id = $1 AND user_id = $2 AND role = $3")
        .bind(tenant_id)
        .bind(user_id)
        .bind(role)
        .execute(executor)
        .await?;
    Ok(())
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct UserRoles {
    pub user_id: Uuid,
    pub roles: Vec<String>,
}

pub async fn list_users<'e>(executor: impl PgExecutor<'e>, tenant_id: Uuid) -> anyhow::Result<Vec<UserRoles>> {
    let rows: Vec<(Uuid, String)> = sqlx::query_as("SELECT user_id, role FROM user_roles WHERE tenant_id = $1")
        .bind(tenant_id)
        .fetch_all(executor)
        .await?;

    let mut grouped: Vec<UserRoles> = Vec::new();
    for (user_id, role) in rows {
        match grouped.iter_mut().find(|u| u.user_id == user_id) {
            Some(existing) => existing.roles.push(role),
            None => grouped.push(UserRoles {
                user_id,
                roles: vec![role],
            }),
        }
    }
    Ok(grouped)
}
