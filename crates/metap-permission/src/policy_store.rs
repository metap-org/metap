//! Mirrors `packages/core/src/core/permission/policy-store.ts`. `PolicyStore` is the one
//! seam this whole permission model already had in TS (see
//! `docs/architecture-review-2026-08-07.md` Part 2's `EventBus` recommendation, which
//! generalizes this exact precedent) — kept as a trait here for the same reason: swapping
//! storage without touching `PermissionSnapshot`/`PermissionService`.

use async_trait::async_trait;
use sqlx::types::Json;
use sqlx::{PgPool, Row};
use uuid::Uuid;

use crate::policy_condition::PolicyCondition;

#[derive(Debug, Clone)]
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
}

fn row_from_sql(row: &sqlx::postgres::PgRow) -> anyhow::Result<PolicyRow> {
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
    ) -> anyhow::Result<PolicyRow>;

    async fn delete_policy(&self, tenant_id: Uuid, id: Uuid) -> anyhow::Result<()>;
}

pub struct PostgresPolicyStore {
    pool: PgPool,
}

impl PostgresPolicyStore {
    pub fn new(pool: PgPool) -> Self {
        Self { pool }
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
        let rows = sqlx::query(
            "SELECT * FROM policies WHERE tenant_id = $1 AND entity = $2 AND action = $3 \
             AND field IS NULL AND subject = 'context'",
        )
        .bind(tenant_id)
        .bind(entity)
        .bind(action)
        .fetch_all(&self.pool)
        .await?;
        rows.iter().map(row_from_sql).collect()
    }

    async fn load_all_policies(&self, tenant_id: Uuid, entity: &str) -> anyhow::Result<Vec<PolicyRow>> {
        let rows = sqlx::query("SELECT * FROM policies WHERE tenant_id = $1 AND entity = $2")
            .bind(tenant_id)
            .bind(entity)
            .fetch_all(&self.pool)
            .await?;
        rows.iter().map(row_from_sql).collect()
    }

    async fn find_explain_policies(
        &self,
        tenant_id: Uuid,
        entity: &str,
        action: &str,
        options: &ExplainOptions,
    ) -> anyhow::Result<Vec<PolicyRow>> {
        let rows = if let Some(field) = &options.field {
            sqlx::query(
                "SELECT * FROM policies WHERE tenant_id = $1 AND entity = $2 AND action = $3 AND field = $4",
            )
            .bind(tenant_id)
            .bind(entity)
            .bind(action)
            .bind(field)
            .fetch_all(&self.pool)
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
            .fetch_all(&self.pool)
            .await?
        };
        rows.iter().map(row_from_sql).collect()
    }

    async fn list_policies(&self, tenant_id: Uuid, entity: Option<&str>) -> anyhow::Result<Vec<PolicyRow>> {
        let rows = if let Some(entity) = entity {
            sqlx::query("SELECT * FROM policies WHERE tenant_id = $1 AND entity = $2")
                .bind(tenant_id)
                .bind(entity)
                .fetch_all(&self.pool)
                .await?
        } else {
            sqlx::query("SELECT * FROM policies WHERE tenant_id = $1")
                .bind(tenant_id)
                .fetch_all(&self.pool)
                .await?
        };
        rows.iter().map(row_from_sql).collect()
    }

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
    ) -> anyhow::Result<PolicyRow> {
        let subject = subject.unwrap_or(PolicySubject::Context).as_str();
        let row = sqlx::query(
            "INSERT INTO policies (tenant_id, entity, action, roles, condition, created_by, field, subject) \
             VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING *",
        )
        .bind(tenant_id)
        .bind(entity)
        .bind(action)
        .bind(roles.map(Json))
        .bind(condition.map(Json))
        .bind(created_by)
        .bind(field)
        .bind(subject)
        .fetch_one(&self.pool)
        .await?;
        row_from_sql(&row)
    }

    async fn delete_policy(&self, tenant_id: Uuid, id: Uuid) -> anyhow::Result<()> {
        sqlx::query("DELETE FROM policies WHERE tenant_id = $1 AND id = $2")
            .bind(tenant_id)
            .bind(id)
            .execute(&self.pool)
            .await?;
        Ok(())
    }
}
