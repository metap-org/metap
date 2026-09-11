//! E2E test against the repo's real dev Postgres (see `CLAUDE.md`'s Commands section:
//! `docker compose up -d postgres`, `pnpm db:migrate`). `#[ignore]`d so a plain `cargo test`
//! never touches a database — run with `cargo test -p metap-control -- --ignored`.
//!
//! `provision_dedicated_db_tenant`'s "dedicated" database used to be the *same* `DATABASE_URL`
//! this test connects to — found live (2026-08-27): that function runs `DROP SCHEMA control
//! CASCADE` against whatever it migrates (see its own doc comment: a dedicated tenant's own DB
//! shouldn't carry a redundant `control` schema), so reusing the shared database here meant
//! this test dropped `control.tenants` out from under every *other* e2e test concurrently
//! relying on it — latent for a while (this test and everything racing it had to actually
//! overlap in time), then a real collision in CI once the workspace grew enough e2e test
//! crates to shift `cargo test --workspace -- --ignored`'s timing (`relation control.tenants
//! does not exist` mid-run). Fixed at the root: `create_throwaway_database`/
//! `drop_throwaway_database` below give this one test a genuinely separate database on the
//! same server, so the drop can never touch anything another test needs. `router_postgres.rs`'s
//! dedicated-db tests don't need the same fix — they insert into `control.tenants` directly and
//! never call `provision_dedicated_db_tenant`, so they never run that drop.

use std::sync::Arc;

use metap_control::{
    EnvStore, PostgresTenantRegistry, RegistryCache, Router, RouterError, TenantId, TenantRegistry, TenantStrategy,
};
use sqlx::postgres::PgPoolOptions;
use uuid::Uuid;

async fn connect() -> sqlx::PgPool {
    let database_url = std::env::var("DATABASE_URL").expect("DATABASE_URL required for this e2e test");
    PgPoolOptions::new()
        .max_connections(3)
        .connect(&database_url)
        .await
        .expect("connect to dev postgres")
}

/// Creates a throwaway database on the same Postgres server `DATABASE_URL` points at, connected
/// to via the always-present `postgres` maintenance database (you can't `CREATE`/`DROP DATABASE`
/// against the database you're currently connected to). Returns the new database's own
/// connection URL.
async fn create_throwaway_database(database_url: &str, name: &str) -> String {
    let (base, _dbname) = database_url
        .rsplit_once('/')
        .expect("DATABASE_URL must end in /<dbname>");
    let admin_pool = PgPoolOptions::new()
        .max_connections(1)
        .connect(&format!("{base}/postgres"))
        .await
        .expect("connect to postgres maintenance db");
    sqlx::query(&format!("CREATE DATABASE \"{name}\""))
        .execute(&admin_pool)
        .await
        .expect("create throwaway database");
    admin_pool.close().await;
    format!("{base}/{name}")
}

async fn drop_throwaway_database(database_url: &str, name: &str) {
    let (base, _dbname) = database_url
        .rsplit_once('/')
        .expect("DATABASE_URL must end in /<dbname>");
    let Ok(admin_pool) = PgPoolOptions::new()
        .max_connections(1)
        .connect(&format!("{base}/postgres"))
        .await
    else {
        return;
    };
    // `WITH (FORCE)` (Postgres 13+) disconnects any lingering session first — best-effort
    // cleanup, not asserted, so a leaked connection from the test itself can't fail the test.
    let _ = sqlx::query(&format!("DROP DATABASE IF EXISTS \"{name}\" WITH (FORCE)"))
        .execute(&admin_pool)
        .await;
}

async fn cleanup(pool: &sqlx::PgPool, tenant_id: Uuid) {
    // `provision_schema_tenant` now creates a real `t_<id>` schema (own `users`/`user_roles`/...)
    // rather than writing into the shared `public`/`metadata` tables these two DELETEs target —
    // harmless no-ops against a real per-tenant-schema tenant, still needed for anything that
    // inserted straight into `control.tenants` without going through provisioning (this file's
    // `single_connection_pool_never_leaks_search_path_between_two_registered_tenants`-style
    // helpers elsewhere use their own schema names, unaffected by this).
    sqlx::query("DELETE FROM user_roles WHERE tenant_id = $1")
        .bind(tenant_id)
        .execute(pool)
        .await
        .ok();
    sqlx::query("DELETE FROM users WHERE tenant_id = $1")
        .bind(tenant_id)
        .execute(pool)
        .await
        .ok();
    sqlx::query(&format!("DROP SCHEMA IF EXISTS \"t_{}\" CASCADE", tenant_id.simple()))
        .execute(pool)
        .await
        .ok();
    sqlx::query("DELETE FROM control.tenants WHERE id = $1")
        .bind(tenant_id)
        .execute(pool)
        .await
        .ok();
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn provision_schema_tenant_writes_registry_row_and_admin_user() {
    let pool = connect().await;
    let registry = PostgresTenantRegistry::new(pool.clone());
    let tenant_id = Uuid::new_v4();

    let provisioned = metap_control::provision_schema_tenant(
        &pool,
        &registry,
        tenant_id,
        &format!("admin-{}@test.local", tenant_id.simple()),
        "pass123",
    )
    .await
    .expect("provision_schema_tenant");
    assert_eq!(provisioned.tenant_id, tenant_id);

    let routing = registry
        .get(TenantId(tenant_id))
        .await
        .expect("get")
        .expect("tenant row exists");
    let expected_schema = format!("t_{}", tenant_id.simple());
    assert!(
        matches!(&routing.strategy, TenantStrategy::Schema { schema_name } if *schema_name == expected_schema),
        "real per-tenant schema isolation: schema_name must be \"{expected_schema}\", not \"public\" — got {:?}",
        routing.strategy
    );

    // The admin user's role now lives in this tenant's own cloned `user_roles`, not the shared
    // `metadata.user_roles` — query it schema-qualified rather than through the bare pool's
    // default search_path (which would silently find `metadata.user_roles` instead and miss
    // this entirely, since `public` isn't even the fallback here — a real per-tenant schema is).
    let roles: Vec<String> = sqlx::query_scalar(&format!(
        "SELECT role FROM \"{expected_schema}\".user_roles WHERE tenant_id = $1 AND user_id = $2"
    ))
    .bind(tenant_id)
    .bind(provisioned.admin_user_id)
    .fetch_all(&pool)
    .await
    .expect("fetch roles");
    assert_eq!(roles, vec!["admin"]);

    cleanup(&pool, tenant_id).await;
}

/// Real per-tenant schema isolation (`../metap-docs/docs/features/35-*.md`): two tenants
/// provisioned via `provision_schema_tenant` each get their own physical copy of every
/// tenant-scoped table, not just a `tenant_id` filter on shared tables. Writes a `policies` row
/// into each tenant's own schema through `Router::begin`'s ordinary tenant-scoped transaction
/// (the real path every HTTP request uses, not a schema-qualified test shortcut) and confirms
/// each only ever sees its own row. Also confirms the 3 FK constraints `create_tenant_schema`
/// adds by hand actually work in a fresh tenant schema, since Postgres's `CREATE TABLE (LIKE
/// ...)` never copies foreign keys under any `INCLUDING` option.
#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn provision_schema_tenant_creates_isolated_real_tables_with_working_fks() {
    let pool = connect().await;
    let registry = PostgresTenantRegistry::new(pool.clone());
    let tenant_a = Uuid::new_v4();
    let tenant_b = Uuid::new_v4();

    metap_control::provision_schema_tenant(&pool, &registry, tenant_a, "a@fks.test.local", "pass123")
        .await
        .expect("provision a");
    metap_control::provision_schema_tenant(&pool, &registry, tenant_b, "b@fks.test.local", "pass123")
        .await
        .expect("provision b");

    let router = Router::new(
        pool.clone(),
        RegistryCache::new(Arc::new(PostgresTenantRegistry::new(pool.clone()))),
        Arc::new(EnvStore),
    );

    let mut tx_a = router.begin(TenantId(tenant_a)).await.expect("begin tenant A");
    sqlx::query(
        "INSERT INTO policies (id, tenant_id, entity, action, effect) VALUES (gen_random_uuid(), $1, 'x', 'read', 'allow')",
    )
    .bind(tenant_a)
    .execute(&mut *tx_a)
    .await
    .expect("insert policy for tenant A");
    tx_a.commit().await.expect("commit A");

    // Tenant B's own transaction must see zero policies — not tenant A's row leaking through a
    // shared table, and not a stale search_path from whichever connection served tenant A.
    let mut tx_b = router.begin(TenantId(tenant_b)).await.expect("begin tenant B");
    let b_policy_count: i64 = sqlx::query_scalar("SELECT count(*) FROM policies")
        .fetch_one(&mut *tx_b)
        .await
        .expect("count policies for tenant B");
    assert_eq!(b_policy_count, 0, "tenant B must not see tenant A's policy row");
    tx_b.commit().await.expect("commit B");

    // FK enforcement: a cron_job_run referencing a real cron_job in the *same* tenant schema
    // succeeds; referencing a nonexistent id is rejected — proves the hand-added FK constraint
    // (never copied by `LIKE`) is actually there and actually enforced, not just declared.
    let mut tx_a2 = router.begin(TenantId(tenant_a)).await.expect("begin tenant A again");
    let job_id: Uuid = sqlx::query_scalar(
        "INSERT INTO cron_jobs (id, tenant_id, name, target_type, target_config) \
         VALUES (gen_random_uuid(), $1, 'test', 'webhook', '{}'::jsonb) RETURNING id",
    )
    .bind(tenant_a)
    .fetch_one(&mut *tx_a2)
    .await
    .expect("insert cron_job");
    sqlx::query(
        "INSERT INTO cron_job_runs (id, tenant_id, job_id, status, scheduled_for) \
         VALUES (gen_random_uuid(), $1, $2, 'running', now())",
    )
    .bind(tenant_a)
    .bind(job_id)
    .execute(&mut *tx_a2)
    .await
    .expect("insert cron_job_run referencing a real cron_job must succeed");

    let rejected = sqlx::query(
        "INSERT INTO cron_job_runs (id, tenant_id, job_id, status, scheduled_for) \
         VALUES (gen_random_uuid(), $1, gen_random_uuid(), 'running', now())",
    )
    .bind(tenant_a)
    .execute(&mut *tx_a2)
    .await;
    assert!(
        rejected.is_err(),
        "cron_job_runs.job_id FK must be enforced in a fresh tenant schema — LIKE never copies FKs"
    );
    tx_a2.rollback().await.ok();

    cleanup(&pool, tenant_a).await;
    cleanup(&pool, tenant_b).await;
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn provision_dedicated_db_tenant_migrates_and_creates_admin() {
    let pool = connect().await;
    let database_url = std::env::var("DATABASE_URL").expect("DATABASE_URL required for this e2e test");
    let registry = PostgresTenantRegistry::new(pool.clone());
    let tenant_id = Uuid::new_v4();
    let dsn_secret_ref = format!("METAP_TEST_PROVISION_DSN_{}", tenant_id.simple());

    let db_name = format!("test_dedicated_{}", tenant_id.simple());
    let dedicated_url = create_throwaway_database(&database_url, &db_name).await;

    let provisioned = metap_control::provision_dedicated_db_tenant(
        &registry,
        tenant_id,
        &dsn_secret_ref,
        &dedicated_url,
        &format!("admin-{}@test.local", tenant_id.simple()),
        "pass123",
    )
    .await
    .expect("provision_dedicated_db_tenant");

    let routing = registry
        .get(TenantId(tenant_id))
        .await
        .expect("get")
        .expect("tenant row exists");
    assert!(matches!(routing.strategy, TenantStrategy::DedicatedDb { dsn_secret_ref: r } if r == dsn_secret_ref));

    // The admin user lives on the dedicated database now that it's genuinely separate, not the
    // shared `pool` above (which only ever holds `control.tenants`/the registry for this test).
    let dedicated_pool = PgPoolOptions::new()
        .max_connections(1)
        .connect(&dedicated_url)
        .await
        .expect("connect to dedicated database");
    let email: String = sqlx::query_scalar("SELECT email FROM users WHERE id = $1")
        .bind(provisioned.admin_user_id)
        .fetch_one(&dedicated_pool)
        .await
        .expect("admin user exists");
    assert!(email.starts_with("admin-"));
    dedicated_pool.close().await;

    drop_throwaway_database(&database_url, &db_name).await;
    cleanup(&pool, tenant_id).await;
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn list_returns_every_provisioned_tenant() {
    let pool = connect().await;
    let registry = PostgresTenantRegistry::new(pool.clone());
    let tenant_a = Uuid::new_v4();
    let tenant_b = Uuid::new_v4();

    metap_control::provision_schema_tenant(&pool, &registry, tenant_a, "a@test.local", "pass123")
        .await
        .expect("provision a");
    metap_control::provision_schema_tenant(&pool, &registry, tenant_b, "b@test.local", "pass123")
        .await
        .expect("provision b");

    let summaries = registry.list().await.expect("list");
    let ids: Vec<Uuid> = summaries.iter().map(|s| s.id).collect();
    assert!(ids.contains(&tenant_a));
    assert!(ids.contains(&tenant_b));

    cleanup(&pool, tenant_a).await;
    cleanup(&pool, tenant_b).await;
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn provisioning_a_duplicate_tenant_id_fails_with_a_downcastable_unique_violation() {
    let pool = connect().await;
    let registry = PostgresTenantRegistry::new(pool.clone());
    let tenant_id = Uuid::new_v4();

    metap_control::provision_schema_tenant(&pool, &registry, tenant_id, "first@test.local", "pass123")
        .await
        .expect("first provision");

    let err = metap_control::provision_schema_tenant(&pool, &registry, tenant_id, "second@test.local", "pass123")
        .await
        .expect_err("duplicate tenantId must fail");

    // Same downcast `metap-control-http`'s `duplicate_tenant_id_response` relies on to map
    // this to a clean 409 instead of a generic 500 — regression test for that assumption.
    let sqlx_err = err
        .downcast_ref::<sqlx::Error>()
        .expect("error must still be downcastable to sqlx::Error");
    let sqlx::Error::Database(db_err) = sqlx_err else {
        panic!("expected a database error, got {sqlx_err:?}");
    };
    assert!(db_err.is_unique_violation());

    cleanup(&pool, tenant_id).await;
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn set_status_to_suspended_is_immediately_enforced_by_router() {
    // Regression test tying `PostgresTenantRegistry::set_status` to the enforcement it relies
    // on already existing (`Router::begin` rejecting `Suspended` — `RouterError`) — a fresh
    // `RegistryCache` here (not the 30s-TTL one a real server holds across requests) means this
    // sees the write immediately, so this only proves the write + the Router check are both
    // correct, not the cache staleness tradeoff `set_status`'s doc comment already documents.
    let pool = connect().await;
    let registry = PostgresTenantRegistry::new(pool.clone());
    let tenant_id = Uuid::new_v4();

    metap_control::provision_schema_tenant(&pool, &registry, tenant_id, "suspend-test@test.local", "pass123")
        .await
        .expect("provision");

    let updated = registry.set_status(tenant_id, "suspended").await.expect("set_status");
    assert!(updated);

    let router = Router::new(
        pool.clone(),
        RegistryCache::new(Arc::new(PostgresTenantRegistry::new(pool.clone()))),
        Arc::new(EnvStore),
    );
    let err = router
        .begin(TenantId(tenant_id))
        .await
        .expect_err("suspended tenant must be rejected");
    assert!(matches!(
        err.downcast_ref::<RouterError>(),
        Some(RouterError::TenantSuspended)
    ));

    let unknown = registry.set_status(Uuid::new_v4(), "active").await.expect("set_status");
    assert!(
        !unknown,
        "set_status on an unknown id must report no row updated, not error"
    );

    cleanup(&pool, tenant_id).await;
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn deprovisioning_is_immediately_enforced_by_router_with_a_404_not_a_403() {
    // Same shape as the suspend test above, but deprovisioning is one-way and 404s (the tenant
    // no longer exists, as far as Router is concerned) instead of suspend's 403 (exists, but
    // temporarily forbidden and resumable).
    let pool = connect().await;
    let registry = PostgresTenantRegistry::new(pool.clone());
    let tenant_id = Uuid::new_v4();

    metap_control::provision_schema_tenant(&pool, &registry, tenant_id, "deprovision-test@test.local", "pass123")
        .await
        .expect("provision");

    let updated = registry.deprovision(tenant_id).await.expect("deprovision");
    assert!(updated);

    let router = Router::new(
        pool.clone(),
        RegistryCache::new(Arc::new(PostgresTenantRegistry::new(pool.clone()))),
        Arc::new(EnvStore),
    );
    let err = router
        .begin(TenantId(tenant_id))
        .await
        .expect_err("deleted tenant must be rejected");
    assert!(matches!(
        err.downcast_ref::<RouterError>(),
        Some(RouterError::TenantDeleted)
    ));

    // Idempotent: deprovisioning an already-deleted tenant is a no-op, not an error, and still
    // reports a row was matched (`true`), same as `set_status` would for any other value.
    let updated_again = registry.deprovision(tenant_id).await.expect("second deprovision");
    assert!(updated_again);

    let unknown = registry
        .deprovision(Uuid::new_v4())
        .await
        .expect("deprovision unknown id");
    assert!(
        !unknown,
        "deprovision on an unknown id must report no row updated, not error"
    );

    cleanup(&pool, tenant_id).await;
}
