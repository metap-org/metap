//! Tenant provisioning — the two flows `dev-tools provision-tenant` (CLI) has always run
//! inline, pulled out here (Phase 16 Giai đoạn 3, `docs/roadmap.md`) so a new `POST
//! /platform/tenants` HTTP handler (`metap-control-http`) can call the exact same functions
//! instead of a second, hand-copied implementation. Same reasoning `metap-peripherals::mint_jwt`/
//! `create_user` already established: a CLI-provisioned tenant and an HTTP-provisioned one can't
//! diverge if there's only one function that does the provisioning.

use sqlx::{PgExecutor, PgPool};
use uuid::Uuid;

use crate::registry::PostgresTenantRegistry;

#[derive(Debug, Clone, Copy)]
pub struct ProvisionedTenant {
    pub tenant_id: Uuid,
    pub admin_user_id: Uuid,
}

/// Every newly provisioned tenant gets `local` (password) auth enabled by default — the only
/// provider that has ever existed (`crates/migrations/0019_tenant_auth_configs.sql` backfills the
/// same row for tenants provisioned before this table existed). `metap-auth`'s doc comment: a
/// tenant can have more than one provider enabled, this is just what every tenant starts with.
async fn seed_local_auth_config<'e>(executor: impl PgExecutor<'e>, tenant_id: Uuid) -> anyhow::Result<()> {
    sqlx::query(
        "INSERT INTO tenant_auth_configs (tenant_id, provider_kind, enabled, config) \
         VALUES ($1, 'local', true, '{}'::jsonb) \
         ON CONFLICT (tenant_id, provider_kind) DO NOTHING",
    )
    .bind(tenant_id)
    .execute(executor)
    .await?;
    Ok(())
}

/// Trial tier — real per-tenant schema isolation (`../metap-docs/docs/features/35-*.md`):
/// `schema_name` is `"t_" + tenant_id` (`Uuid::simple()`, 32 lowercase hex chars — satisfies
/// `Router::validate_schema_name`'s `^t_[a-z0-9]+$` whitelist by construction, deterministic,
/// collision-free 1:1 with the tenant's own id), not the old hardcoded `"public"`.
/// `crate::tenant_schema::create_tenant_schema` does the DDL — creates the schema and clones
/// every tenant-scoped table into it — **before** the `control.tenants` row is written, same
/// ordering `provision_dedicated_db_tenant` below already uses: finish all setup, then make the
/// tenant visible to `Router` in one atomic insert, never a row that exists before its schema
/// does. `shared_pool` is still the main database's pool (this tenant's data now lives in its own
/// schema *within* that same database, not a separate one — that's `DedicatedDb`'s job) — but
/// seeding the admin user/auth config needs to run against a connection whose `search_path`
/// actually points at the new schema first, since those helpers issue unqualified queries
/// (`tenant_auth_configs`/`users`/`user_roles`) that would otherwise resolve against the shared
/// pool's own default (`public`), same mistake `pool_for`'s doc comment already warns about.
pub async fn provision_schema_tenant(
    shared_pool: &PgPool,
    registry: &PostgresTenantRegistry,
    tenant_id: Uuid,
    admin_email: &str,
    admin_password: &str,
) -> anyhow::Result<ProvisionedTenant> {
    let schema_name = format!("t_{}", tenant_id.simple());
    crate::tenant_schema::create_tenant_schema(shared_pool, &schema_name).await?;

    // A dedicated connection, not the pool directly — `SET` (not `SET LOCAL`) is fine only
    // because this connection is acquired, used for this provisioning call alone, and dropped
    // (never returned to serve an unrelated request), same reasoning
    // `provision_dedicated_db_tenant`'s own `SET search_path` comment gives.
    let mut conn = shared_pool.acquire().await?;
    sqlx::query(&format!("SET search_path TO \"{schema_name}\", metadata, control"))
        .execute(&mut *conn)
        .await?;
    seed_local_auth_config(&mut *conn, tenant_id).await?;
    let user = metap_peripherals::create_user(&mut *conn, tenant_id, admin_email, admin_password).await?;
    metap_peripherals::assign_role(&mut *conn, tenant_id, user.id, "admin", None).await?;
    drop(conn);

    registry
        .provision(tenant_id, "trial", "schema", Some(&schema_name), None, "active")
        .await?;

    Ok(ProvisionedTenant {
        tenant_id,
        admin_user_id: user.id,
    })
}

/// Paid tier — migrates `dedicated_database_url` (a fresh Postgres database) with the same
/// `crates/migrations/*.sql` the main database runs, so `records`/`users`/... exist there too,
/// then writes the `control.tenants` row (via `registry`, which already wraps the main
/// database's pool — the registry itself is always control-plane data, never tenant-scoped)
/// and creates the admin user on the dedicated database. Caller is responsible for making
/// `dsn_secret_ref` resolvable (`Router`'s `EnvStore` reads it as an env var name) before this
/// tenant is actually routed to — this function only writes the registry row, it doesn't touch
/// the running process's environment.
pub async fn provision_dedicated_db_tenant(
    registry: &PostgresTenantRegistry,
    tenant_id: Uuid,
    dsn_secret_ref: &str,
    dedicated_database_url: &str,
    admin_email: &str,
    admin_password: &str,
) -> anyhow::Result<ProvisionedTenant> {
    let dedicated_pool = sqlx::postgres::PgPoolOptions::new()
        .max_connections(1)
        .connect(dedicated_database_url)
        .await?;
    sqlx::migrate!("../migrations").run(&dedicated_pool).await?;
    // `0028_metadata_schema.sql` (run just above) sets the database's own default
    // `search_path` for *new* connections — this pool's one connection (`max_connections(1)`)
    // was already open before that ran, so it keeps whatever `search_path` it started with
    // unless told otherwise here. A plain `SET` (not `SET LOCAL`) is fine — this pool is
    // private to this one provisioning call, never returned to a shared application pool.
    sqlx::query("SET search_path TO public, metadata, control")
        .execute(&dedicated_pool)
        .await?;
    // `control.tenants` (`0012_control_tenants.sql`) is genuinely global platform data — the
    // one registry every tenant is looked up through — never tenant-scoped data itself
    // (real feedback: this used to leave an empty, unused `control` schema baked into every
    // dedicated tenant's own database, which made "which DB is the platform's registry
    // actually in" ambiguous just from looking at one). There's no way to skip a migration
    // file from `sqlx::migrate!`'s embedded set, so drop it right back out post-migrate — the
    // dedicated DB's own `_sqlx_migrations` history still records 0012 as applied, which is
    // correct (it *did* run); nothing here ever migrates that table again.
    sqlx::query("DROP SCHEMA IF EXISTS control CASCADE")
        .execute(&dedicated_pool)
        .await?;

    registry
        .provision(tenant_id, "paid", "dedicated_db", None, Some(dsn_secret_ref), "active")
        .await?;
    seed_local_auth_config(&dedicated_pool, tenant_id).await?;
    let user = metap_peripherals::create_user(&dedicated_pool, tenant_id, admin_email, admin_password).await?;
    metap_peripherals::assign_role(&dedicated_pool, tenant_id, user.id, "admin", None).await?;

    Ok(ProvisionedTenant {
        tenant_id,
        admin_user_id: user.id,
    })
}
