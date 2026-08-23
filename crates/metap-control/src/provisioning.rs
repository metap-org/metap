//! Tenant provisioning — the two flows `dev-tools provision-tenant` (CLI) has always run
//! inline, pulled out here (Phase 16 Giai đoạn 3, `docs/roadmap.md`) so a new `POST
//! /platform/tenants` HTTP handler (`metap-control-http`) can call the exact same functions
//! instead of a second, hand-copied implementation. Same reasoning `metap-peripherals::mint_jwt`/
//! `create_user` already established: a CLI-provisioned tenant and an HTTP-provisioned one can't
//! diverge if there's only one function that does the provisioning.

use sqlx::PgPool;
use uuid::Uuid;

use crate::registry::PostgresTenantRegistry;

#[derive(Debug, Clone, Copy)]
pub struct ProvisionedTenant {
    pub tenant_id: Uuid,
    pub admin_user_id: Uuid,
}

/// Trial tier — `schema_name` is always pinned to `"public"` (see `Router::begin`'s doc
/// comment and `docs/roadmap.md` Phase 16: real per-tenant schema isolation needs
/// table-per-entity, not built yet). `shared_pool` is the main database's pool — the admin
/// user this creates lives there too, since `strategy: Schema` tenants never leave `public`.
pub async fn provision_schema_tenant(
    shared_pool: &PgPool,
    registry: &PostgresTenantRegistry,
    tenant_id: Uuid,
    admin_email: &str,
    admin_password: &str,
) -> anyhow::Result<ProvisionedTenant> {
    registry
        .provision(tenant_id, "trial", "schema", Some("public"), None, "active")
        .await?;
    let user = metap_peripherals::create_user(shared_pool, tenant_id, admin_email, admin_password).await?;
    metap_peripherals::assign_role(shared_pool, tenant_id, user.id, "admin", None).await?;

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
    let user = metap_peripherals::create_user(&dedicated_pool, tenant_id, admin_email, admin_password).await?;
    metap_peripherals::assign_role(&dedicated_pool, tenant_id, user.id, "admin", None).await?;

    Ok(ProvisionedTenant {
        tenant_id,
        admin_user_id: user.id,
    })
}
