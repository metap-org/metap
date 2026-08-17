//! `TenantRegistry` is a trait, not a concrete type, for the same reason `PolicyStore`
//! (`crates/metap-permission/src/policy_store.rs`) is — swapping storage without touching
//! `Router`/`RegistryCache`.

use async_trait::async_trait;
use chrono::{DateTime, Utc};
use sqlx::{PgPool, Row};
use uuid::Uuid;

use crate::tenant::{TenantId, TenantRouting, TenantStatus, TenantStrategy};

#[async_trait]
pub trait TenantRegistry: Send + Sync {
    async fn get(&self, tenant: TenantId) -> anyhow::Result<Option<TenantRouting>>;
}

/// One `control.tenants` row, for listing (`GET /platform/tenants`, Phase 16 Giai đoạn 3) —
/// unlike `TenantRouting` (which `Router` needs and nothing else), this carries the columns an
/// operator actually wants to see: `id`/`tier`/`created_at`/`trial_expires_at` alongside the
/// same `strategy`/`status` shape `get` already parses.
#[derive(Debug, Clone)]
pub struct TenantSummary {
    pub id: Uuid,
    pub tier: String,
    pub strategy: TenantStrategy,
    pub status: TenantStatus,
    pub created_at: DateTime<Utc>,
    pub trial_expires_at: Option<DateTime<Utc>>,
}

fn parse_strategy(
    strategy_col: &str,
    schema_name: Option<String>,
    dsn_secret_ref: Option<String>,
) -> anyhow::Result<TenantStrategy> {
    match strategy_col {
        "schema" => Ok(TenantStrategy::Schema {
            schema_name: schema_name
                .ok_or_else(|| anyhow::anyhow!("control.tenants row with strategy='schema' has no schema_name"))?,
        }),
        "dedicated_db" => Ok(TenantStrategy::DedicatedDb {
            dsn_secret_ref: dsn_secret_ref.ok_or_else(|| {
                anyhow::anyhow!("control.tenants row with strategy='dedicated_db' has no dsn_secret_ref")
            })?,
        }),
        other => anyhow::bail!("unknown control.tenants.strategy value: {other}"),
    }
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

    /// Flips `status` on an existing tenant — the only mutation `PATCH /platform/tenants/{id}/status`
    /// (`metap-control-http`) needs. Enforcement already exists and needs no changes here:
    /// `Router::begin` already rejects `Suspended`/`Expired` with a 403 (`RouterError`,
    /// `crates/metap-crud/src/crud_service.rs`'s `router_unavailable`) — this just writes the
    /// column that check reads. Subject to `RegistryCache`'s 30s TTL (see that module's doc
    /// comment: "control.tenants changes rarely (provisioning, suspend/promote)... an
    /// acceptable tradeoff"), so a suspend/resume can take up to 30s to actually take effect on
    /// already-cached routing — a deliberate, already-documented tradeoff, not new for this.
    /// Returns `false` (not an error) when `id` doesn't match any row, so the caller can turn
    /// that into a clean 404.
    pub async fn set_status(&self, id: Uuid, status: &str) -> anyhow::Result<bool> {
        let result = sqlx::query("UPDATE control.tenants SET status = $2 WHERE id = $1")
            .bind(id)
            .bind(status)
            .execute(&self.pool)
            .await?;
        Ok(result.rows_affected() > 0)
    }

    /// Every `control.tenants` row, newest first — backs `GET /platform/tenants`
    /// (`metap-control-http`). A row whose `strategy`/`status` value doesn't parse is skipped
    /// with a `tracing::warn!` rather than failing the whole listing — one corrupt row
    /// shouldn't hide every other tenant from an operator trying to see what's provisioned.
    pub async fn list(&self) -> anyhow::Result<Vec<TenantSummary>> {
        let rows = sqlx::query(
            "SELECT id, tier, strategy, schema_name, dsn_secret_ref, status, created_at, trial_expires_at \
             FROM control.tenants ORDER BY created_at DESC",
        )
        .fetch_all(&self.pool)
        .await?;

        let mut summaries = Vec::with_capacity(rows.len());
        for row in rows {
            let id: Uuid = row.try_get("id")?;
            let strategy_col: String = row.try_get("strategy")?;
            let schema_name: Option<String> = row.try_get("schema_name")?;
            let dsn_secret_ref: Option<String> = row.try_get("dsn_secret_ref")?;
            let status_col: String = row.try_get("status")?;

            let (strategy, status) = match (
                parse_strategy(&strategy_col, schema_name, dsn_secret_ref),
                TenantStatus::parse(&status_col),
            ) {
                (Ok(s), Ok(st)) => (s, st),
                (strategy_result, status_result) => {
                    tracing::warn!(tenant_id = %id, ?strategy_result, ?status_result, "skipping unparseable control.tenants row");
                    continue;
                }
            };

            summaries.push(TenantSummary {
                id,
                tier: row.try_get("tier")?,
                strategy,
                status,
                created_at: row.try_get("created_at")?,
                trial_expires_at: row.try_get("trial_expires_at")?,
            });
        }
        Ok(summaries)
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

        let strategy = parse_strategy(&strategy_col, schema_name, dsn_secret_ref)?;

        Ok(Some(TenantRouting {
            status: TenantStatus::parse(&status_col)?,
            strategy,
        }))
    }
}
