//! `TenantRegistry` is a trait, not a concrete type, for the same reason `PolicyStore`
//! (`crates/metap-permission/src/policy_store.rs`) is — swapping storage without touching
//! `Router`/`RegistryCache`.

use async_trait::async_trait;
use sqlx::{PgPool, Row};
use uuid::Uuid;

use crate::tenant::{TenantId, TenantRouting, TenantStatus, TenantStrategy};

#[async_trait]
pub trait TenantRegistry: Send + Sync {
    async fn get(&self, tenant: TenantId) -> anyhow::Result<Option<TenantRouting>>;
}

pub struct PostgresTenantRegistry {
    pool: PgPool,
}

impl PostgresTenantRegistry {
    pub fn new(pool: PgPool) -> Self {
        Self { pool }
    }

    /// Writes a new `control.tenants` row — the provisioning-side counterpart to `get` above.
    /// Not on the `TenantRegistry` trait: only `dev-tools provision-tenant`
    /// (`crates/dev-tools/src/main.rs`) calls this today, and there's no second `TenantRegistry`
    /// impl that would need it. `schema_name`/`dsn_secret_ref` are mutually exclusive depending
    /// on `strategy` — same shape `TenantStrategy` enforces on the read side.
    pub async fn provision(
        &self,
        id: Uuid,
        tier: &str,
        strategy: &str,
        schema_name: Option<&str>,
        dsn_secret_ref: Option<&str>,
        status: &str,
    ) -> anyhow::Result<()> {
        sqlx::query(
            "INSERT INTO control.tenants (id, tier, strategy, schema_name, dsn_secret_ref, status) \
             VALUES ($1, $2, $3, $4, $5, $6)",
        )
        .bind(id)
        .bind(tier)
        .bind(strategy)
        .bind(schema_name)
        .bind(dsn_secret_ref)
        .bind(status)
        .execute(&self.pool)
        .await?;
        Ok(())
    }
}

#[async_trait]
impl TenantRegistry for PostgresTenantRegistry {
    async fn get(&self, tenant: TenantId) -> anyhow::Result<Option<TenantRouting>> {
        let row =
            sqlx::query("SELECT strategy, schema_name, dsn_secret_ref, status FROM control.tenants WHERE id = $1")
                .bind(tenant.0)
                .fetch_optional(&self.pool)
                .await?;

        let Some(row) = row else {
            return Ok(None);
        };

        let strategy_col: String = row.try_get("strategy")?;
        let status_col: String = row.try_get("status")?;
        let schema_name: Option<String> = row.try_get("schema_name")?;
        let dsn_secret_ref: Option<String> = row.try_get("dsn_secret_ref")?;

        let strategy = match strategy_col.as_str() {
            "schema" => TenantStrategy::Schema {
                schema_name: schema_name
                    .ok_or_else(|| anyhow::anyhow!("control.tenants row with strategy='schema' has no schema_name"))?,
            },
            "dedicated_db" => TenantStrategy::DedicatedDb {
                dsn_secret_ref: dsn_secret_ref.ok_or_else(|| {
                    anyhow::anyhow!("control.tenants row with strategy='dedicated_db' has no dsn_secret_ref")
                })?,
            },
            other => anyhow::bail!("unknown control.tenants.strategy value: {other}"),
        };

        Ok(Some(TenantRouting {
            status: TenantStatus::parse(&status_col)?,
            strategy,
        }))
    }
}
