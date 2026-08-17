//! E2E test against the repo's real dev Postgres (see `CLAUDE.md`'s Commands section:
//! `docker compose up -d postgres`, `pnpm db:migrate`). `#[ignore]`d so a plain `cargo test`
//! never touches a database — run with `cargo test -p metap-control -- --ignored`.
//!
//! `provision_dedicated_db_tenant`'s "dedicated" database is the *same* `DATABASE_URL` this
//! test connects to, same limitation `router_postgres.rs`'s dedicated-db tests already accept
//! (see that file's doc comment) — `sqlx::migrate!` is idempotent against an already-migrated
//! database, so this still exercises the real code path (migrate -> registry write -> create
//! user -> assign role), just not genuine physical separation. A real second database is
//! covered by manual smoke testing, not this suite.

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

async fn cleanup(pool: &sqlx::PgPool, tenant_id: Uuid) {
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
    assert!(matches!(routing.strategy, TenantStrategy::Schema { schema_name } if schema_name == "public"));

    let roles: Vec<String> = sqlx::query_scalar("SELECT role FROM user_roles WHERE tenant_id = $1 AND user_id = $2")
        .bind(tenant_id)
        .bind(provisioned.admin_user_id)
        .fetch_all(&pool)
        .await
        .expect("fetch roles");
    assert_eq!(roles, vec!["admin"]);

    cleanup(&pool, tenant_id).await;
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn provision_dedicated_db_tenant_migrates_and_creates_admin() {
    let pool = connect().await;
    let database_url = std::env::var("DATABASE_URL").expect("DATABASE_URL required for this e2e test");
    let registry = PostgresTenantRegistry::new(pool.clone());
    let tenant_id = Uuid::new_v4();
    let dsn_secret_ref = format!("METAP_TEST_PROVISION_DSN_{}", tenant_id.simple());

    let provisioned = metap_control::provision_dedicated_db_tenant(
        &registry,
        tenant_id,
        &dsn_secret_ref,
        &database_url,
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

    let email: String = sqlx::query_scalar("SELECT email FROM users WHERE id = $1")
        .bind(provisioned.admin_user_id)
        .fetch_one(&pool)
        .await
        .expect("admin user exists");
    assert!(email.starts_with("admin-"));

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
