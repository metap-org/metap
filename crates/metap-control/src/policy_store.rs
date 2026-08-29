//! `Router`-backed `metap_permission::PolicyStore` impl (`docs/roadmap.md` Phase 16 gap,
//! closed 2026-08-20). Lives here rather than in `metap-permission` itself purely to avoid a
//! dependency cycle: `metap-metadata -> metap-permission`, `metap-peripherals ->
//! metap-metadata`, `metap-control -> metap-peripherals` — `metap-permission -> metap-control`
//! would close the loop. `metap-control -> metap-permission` (this file's only reason for that
//! dependency) has no such problem since `metap-permission` depends on nothing in this chain.
//!
//! Routes every query through `Router::begin(tenant_id)` instead of a fixed `PgPool` — a
//! `DedicatedDb`-strategy tenant's `policies` table lives only in that tenant's own database,
//! never in the shared control-plane pool this used to be pinned to. Every `PolicyStore` method
//! already takes `tenant_id: Uuid` as its first parameter, so no trait signature change was
//! needed, only the storage backing it.

use async_trait::async_trait;
use metap_permission::{row_from_sql, ExplainOptions, PolicyEffect, PolicyRow, PolicyStore, PolicySubject};
use sqlx::types::Json;
use uuid::Uuid;

use crate::router::Router;

pub struct PostgresPolicyStore {
    router: Router,
}

impl PostgresPolicyStore {
    pub fn new(router: Router) -> Self {
        Self { router }
    }
}

#[async_trait]
impl PolicyStore for PostgresPolicyStore {
    async fn find_context_policies(
        &self,
        tenant_id: Uuid,
        entity: &str,
        action: &str,
    ) -> anyhow::Result<Vec<PolicyRow>> {
        let mut tx = self.router.begin(tenant_id.into()).await?;
        let rows = sqlx::query(
            "SELECT * FROM policies WHERE tenant_id = $1 AND entity = $2 AND action = $3 \
             AND field IS NULL AND subject = 'context'",
        )
        .bind(tenant_id)
        .bind(entity)
        .bind(action)
        .fetch_all(&mut *tx)
        .await?;
        tx.commit().await?;
        rows.iter().map(row_from_sql).collect()
    }

    async fn load_all_policies(&self, tenant_id: Uuid, entity: &str) -> anyhow::Result<Vec<PolicyRow>> {
        let mut tx = self.router.begin(tenant_id.into()).await?;
        let rows = sqlx::query("SELECT * FROM policies WHERE tenant_id = $1 AND entity = $2")
            .bind(tenant_id)
            .bind(entity)
            .fetch_all(&mut *tx)
            .await?;
        tx.commit().await?;
        rows.iter().map(row_from_sql).collect()
    }

    async fn find_explain_policies(
        &self,
        tenant_id: Uuid,
        entity: &str,
        action: &str,
        options: &ExplainOptions,
    ) -> anyhow::Result<Vec<PolicyRow>> {
        let mut tx = self.router.begin(tenant_id.into()).await?;
        let rows = if let Some(field) = &options.field {
            sqlx::query("SELECT * FROM policies WHERE tenant_id = $1 AND entity = $2 AND action = $3 AND field = $4")
                .bind(tenant_id)
                .bind(entity)
                .bind(action)
                .bind(field)
                .fetch_all(&mut *tx)
                .await?
        } else {
            let subject = options.subject.unwrap_or(PolicySubject::Context).as_str();
            sqlx::query(
                "SELECT * FROM policies WHERE tenant_id = $1 AND entity = $2 AND action = $3 \
                 AND field IS NULL AND subject = $4",
            )
            .bind(tenant_id)
            .bind(entity)
            .bind(action)
            .bind(subject)
            .fetch_all(&mut *tx)
            .await?
        };
        tx.commit().await?;
        rows.iter().map(row_from_sql).collect()
    }

    async fn list_policies(&self, tenant_id: Uuid, entity: Option<&str>) -> anyhow::Result<Vec<PolicyRow>> {
        let mut tx = self.router.begin(tenant_id.into()).await?;
        let rows = if let Some(entity) = entity {
            sqlx::query("SELECT * FROM policies WHERE tenant_id = $1 AND entity = $2")
                .bind(tenant_id)
                .bind(entity)
                .fetch_all(&mut *tx)
                .await?
        } else {
            sqlx::query("SELECT * FROM policies WHERE tenant_id = $1")
                .bind(tenant_id)
                .fetch_all(&mut *tx)
                .await?
        };
        tx.commit().await?;
        rows.iter().map(row_from_sql).collect()
    }

    async fn create_policy(
        &self,
        tenant_id: Uuid,
        entity: &str,
        action: &str,
        roles: Option<Vec<String>>,
        condition: Option<metap_permission::PolicyCondition>,
        created_by: Option<Uuid>,
        field: Option<&str>,
        subject: Option<PolicySubject>,
        effect: PolicyEffect,
    ) -> anyhow::Result<PolicyRow> {
        let subject = subject.unwrap_or(PolicySubject::Context).as_str();
        let mut tx = self.router.begin(tenant_id.into()).await?;
        let row = sqlx::query(
            "INSERT INTO policies (tenant_id, entity, action, roles, condition, created_by, field, subject, effect) \
             VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING *",
        )
        .bind(tenant_id)
        .bind(entity)
        .bind(action)
        .bind(roles.map(Json))
        .bind(condition.map(Json))
        .bind(created_by)
        .bind(field)
        .bind(subject)
        .bind(effect.as_str())
        .fetch_one(&mut *tx)
        .await?;
        tx.commit().await?;
        row_from_sql(&row)
    }

    async fn delete_policy(&self, tenant_id: Uuid, id: Uuid) -> anyhow::Result<()> {
        let mut tx = self.router.begin(tenant_id.into()).await?;
        sqlx::query("DELETE FROM policies WHERE tenant_id = $1 AND id = $2")
            .bind(tenant_id)
            .bind(id)
            .execute(&mut *tx)
            .await?;
        tx.commit().await?;
        Ok(())
    }

    async fn sync_basic_policies(
        &self,
        tenant_id: Uuid,
        entity: &str,
        grants: Vec<(Option<String>, String)>,
        created_by: Option<Uuid>,
    ) -> anyhow::Result<Vec<PolicyRow>> {
        let mut tx = self.router.begin(tenant_id.into()).await?;
        // Delete every existing basic-shaped row for this entity — matches `isBasicShapedRow`
        // on the frontend exactly, so this can never touch an Advanced-tab row (one with a
        // condition, a field scope, `subject = 'record'`, or `effect = 'deny'`).
        sqlx::query(
            "DELETE FROM policies WHERE tenant_id = $1 AND entity = $2 AND condition IS NULL \
             AND field IS NULL AND subject = 'context' AND effect = 'allow'",
        )
        .bind(tenant_id)
        .bind(entity)
        .execute(&mut *tx)
        .await?;

        let mut rows = Vec::with_capacity(grants.len());
        for (role, action) in &grants {
            let roles_json = role.as_ref().map(|r| Json(vec![r.clone()]));
            let row = sqlx::query(
                "INSERT INTO policies (tenant_id, entity, action, roles, condition, created_by, field, subject, effect) \
                 VALUES ($1, $2, $3, $4, NULL, $5, NULL, 'context', 'allow') RETURNING *",
            )
            .bind(tenant_id)
            .bind(entity)
            .bind(action)
            .bind(roles_json)
            .bind(created_by)
            .fetch_one(&mut *tx)
            .await?;
            rows.push(row_from_sql(&row)?);
        }
        tx.commit().await?;
        Ok(rows)
    }
}
