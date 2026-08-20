use std::sync::Arc;
use std::time::Duration;

use secrecy::ExposeSecret;
use sqlx::postgres::PgPoolOptions;
use sqlx::{PgPool, Postgres, Transaction};

use crate::cache::RegistryCache;
use crate::secret_store::SecretStore;
use crate::tenant::{TenantId, TenantRouting, TenantStatus, TenantStrategy};

/// Idle eviction for `Router::dedicated_pools` — a dedicated tenant's pool that hasn't been used
/// in this long is torn down rather than held open forever, since most tenants won't be active
/// at any given moment and each cached pool holds real Postgres connections.
const DEDICATED_POOL_IDLE_TTL: Duration = Duration::from_secs(600);

#[derive(Debug, Clone, PartialEq, Eq)]
pub enum RouterError {
    /// Maps to 403 — an admin suspended the tenant (e.g. non-payment).
    TenantSuspended,
    /// Maps to 503 (retry-able) — a data-plane migration is in flight for this tenant.
    TenantMigrating,
    /// Maps to 503 (retry-able) — provisioning (`docs/multi-tenant-platform-design.md` §2.4)
    /// hasn't finished yet, so the tenant's schema/DB may not exist or be fully seeded.
    TenantProvisioning,
    /// Maps to 403 — trial expired and was never promoted to paid.
    TenantExpired,
    /// `metap-crud::crud_service`'s `router_unavailable` maps this to 404, not 403 — an admin
    /// deprovisioned the tenant (`docs/roadmap.md` Phase 16, `TenantStatus::Deleted`'s doc
    /// comment). Unlike `TenantSuspended`, there is no `resume` path back from this: as far as
    /// anything going through `Router` is concerned, the tenant no longer exists, which 404
    /// communicates correctly and 403 would not. In practice a caller usually never reaches
    /// that mapping for a normal authenticated route, though: `crate::auth::AuthContext`'s
    /// role lookup *also* goes through `Router::begin` for the same tenant id and runs first
    /// (it's an extractor, evaluated before the handler body), collapsing this into a generic
    /// 401 "failed to resolve roles" before `CrudService` is ever reached — same as it already
    /// does for `TenantSuspended`/`TenantExpired`, not a new inconsistency this variant
    /// introduces.
    TenantDeleted,
    /// A `control.tenants` row has a `schema_name` that doesn't pass the whitelist below —
    /// should never happen in practice (rows are only ever written by trusted provisioning
    /// code, never from request input), but `Router::begin` string-interpolates this value
    /// into `SET LOCAL search_path TO ...` (bind parameters aren't valid there), so it is
    /// re-validated on every read as defense-in-depth rather than trusted blindly.
    InvalidSchemaName(String),
}

impl std::fmt::Display for RouterError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            RouterError::TenantSuspended => write!(f, "tenant is suspended"),
            RouterError::TenantMigrating => write!(f, "tenant is mid-migration, retry shortly"),
            RouterError::TenantProvisioning => write!(f, "tenant is still provisioning, retry shortly"),
            RouterError::TenantExpired => write!(f, "tenant's trial has expired"),
            RouterError::TenantDeleted => write!(f, "tenant has been deleted"),
            RouterError::InvalidSchemaName(name) => write!(f, "invalid tenant schema name: {name:?}"),
        }
    }
}

impl std::error::Error for RouterError {}

/// Whitelists what may be interpolated into `SET LOCAL search_path TO {name}` — Postgres DDL-ish
/// session settings don't accept bind parameters, so this is the only thing standing between a
/// corrupted/malicious `control.tenants` row and SQL injection into every tenant-scoped
/// transaction. `"public"` is the legacy/unregistered-tenant fallback (see `Router::begin`);
/// anything else must match `^tenant_[a-z0-9]+$` — matches the schema-naming convention in
/// `docs/multi-tenant-platform-design.md` §2.2's example (`tenant_ab12`).
pub fn validate_schema_name(name: &str) -> Result<(), RouterError> {
    if name == "public" {
        return Ok(());
    }
    let Some(suffix) = name.strip_prefix("tenant_") else {
        return Err(RouterError::InvalidSchemaName(name.to_string()));
    };
    if !suffix.is_empty() && suffix.bytes().all(|b| b.is_ascii_lowercase() || b.is_ascii_digit()) {
        Ok(())
    } else {
        Err(RouterError::InvalidSchemaName(name.to_string()))
    }
}

#[derive(Clone)]
pub struct Router {
    shared_pool: PgPool,
    registry: RegistryCache,
    secret_store: Arc<dyn SecretStore>,
    dedicated_pools: moka::future::Cache<String, Arc<PgPool>>,
}

impl Router {
    pub fn new(shared_pool: PgPool, registry: RegistryCache, secret_store: Arc<dyn SecretStore>) -> Self {
        let dedicated_pools = moka::future::Cache::builder()
            .time_to_idle(DEDICATED_POOL_IDLE_TTL)
            .build();
        Self {
            shared_pool,
            registry,
            secret_store,
            dedicated_pools,
        }
    }

    /// Looks up (or opens and caches) the dedicated `PgPool` for a `DedicatedDb` tenant, keyed
    /// by `dsn_secret_ref` rather than `TenantId` — two tenants could in principle share a
    /// `dsn_secret_ref` only by operator error, but keying on the thing that actually determines
    /// the physical connection (not the logical tenant) is the correct cache key regardless.
    async fn dedicated_pool(&self, dsn_secret_ref: &str) -> anyhow::Result<Arc<PgPool>> {
        let secret_store = self.secret_store.clone();
        let dsn_secret_ref_owned = dsn_secret_ref.to_string();
        self.dedicated_pools
            .try_get_with(dsn_secret_ref_owned.clone(), async move {
                let creds = secret_store.db_credentials(&dsn_secret_ref_owned).await?;
                let pool = PgPoolOptions::new()
                    .max_connections(5)
                    .connect(creds.dsn.expose_secret())
                    .await?;
                anyhow::Ok(Arc::new(pool))
            })
            .await
            .map_err(|e| anyhow::anyhow!("failed to open dedicated pool for {dsn_secret_ref}: {e}"))
    }

    /// Opens a transaction scoped to `tenant` — every query run against the returned
    /// transaction sees that tenant's schema. Callers must `.commit()`/let it roll back
    /// themselves; this never commits on their behalf.
    ///
    /// A tenant with no `control.tenants` row is treated as
    /// `{status: Active, strategy: Schema("public")}` — the pre-Router behavior (single shared
    /// `public` schema, isolation via the `tenant_id` column). This is a deliberate transitional
    /// compatibility shim: nothing provisions `control.tenants` rows yet
    /// (`docs/roadmap.md` Phase 16, tenant provisioning is a later stage), so treating an
    /// unregistered tenant as an error would break every existing tenant/dev flow
    /// (`pnpm mint-token` mints tokens for arbitrary tenant ids with no registration step).
    pub async fn begin(&self, tenant: TenantId) -> anyhow::Result<Transaction<'_, Postgres>> {
        let routing = match self.registry.get(tenant).await? {
            Some(routing) => routing,
            None => {
                tracing::debug!(%tenant, "tenant has no control.tenants row, falling back to public schema");
                TenantRouting {
                    status: TenantStatus::Active,
                    strategy: TenantStrategy::Schema {
                        schema_name: "public".to_string(),
                    },
                }
            }
        };

        match routing.status {
            TenantStatus::Active => {}
            TenantStatus::Suspended => return Err(RouterError::TenantSuspended.into()),
            TenantStatus::Migrating => return Err(RouterError::TenantMigrating.into()),
            TenantStatus::Provisioning => return Err(RouterError::TenantProvisioning.into()),
            TenantStatus::Expired => return Err(RouterError::TenantExpired.into()),
            TenantStatus::Deleted => return Err(RouterError::TenantDeleted.into()),
        }

        match routing.strategy {
            TenantStrategy::Schema { schema_name } => {
                validate_schema_name(&schema_name)?;
                let mut tx = self.shared_pool.begin().await?;
                // `SET LOCAL` = transaction-scoped, cleans up on commit/rollback automatically.
                // Bẫy #1 (docs/multi-tenant-platform-design.md §2.2/§12): must be `SET LOCAL`,
                // never session-level `SET` — a session-level set would leak this tenant's
                // schema to whichever request the pool hands this physical connection to next.
                sqlx::query(&format!("SET LOCAL search_path TO {schema_name}"))
                    .execute(&mut *tx)
                    .await?;
                Ok(tx)
            }
            TenantStrategy::DedicatedDb { dsn_secret_ref } => {
                let pool = self.dedicated_pool(&dsn_secret_ref).await?;
                Ok(pool.begin().await?)
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn validate_schema_name_accepts_whitelisted_forms() {
        assert!(validate_schema_name("public").is_ok());
        assert!(validate_schema_name("tenant_ab12").is_ok());
    }

    #[test]
    fn validate_schema_name_rejects_everything_else() {
        assert!(validate_schema_name("Tenant_AB").is_err());
        assert!(validate_schema_name("tenant_ab;DROP TABLE x--").is_err());
        assert!(validate_schema_name("").is_err());
        assert!(validate_schema_name("public'; select 1--").is_err());
        assert!(validate_schema_name("tenant_").is_err());
    }
}
