//! E2E test against the repo's real dev Postgres (see `CLAUDE.md`'s Commands section:
//! `docker compose up -d postgres`, `pnpm db:migrate` to apply
//! `crates/migrations/0012_control_tenants.sql`). `#[ignore]`d so a plain `cargo test` never
//! touches a database — run with `cargo test -p metap-control -- --ignored`. Unit tests (pure
//! logic, `validate_schema_name`) live in `src/router.rs`.

use std::sync::Arc;

use metap_control::{EnvStore, PostgresTenantRegistry, RegistryCache, Router, RouterError, TenantId};
use sqlx::postgres::PgPoolOptions;
use sqlx::Row;
use uuid::Uuid;

async fn connect() -> sqlx::PgPool {
    let database_url = std::env::var("DATABASE_URL").expect("DATABASE_URL required for this e2e test");
    PgPoolOptions::new()
        .max_connections(3)
        .connect(&database_url)
        .await
        .expect("connect to dev postgres")
}

fn router(pool: sqlx::PgPool) -> Router {
    let registry = Arc::new(PostgresTenantRegistry::new(pool.clone()));
    Router::new(pool, RegistryCache::new(registry), Arc::new(EnvStore))
}

async fn search_path(tx: &mut sqlx::Transaction<'_, sqlx::Postgres>) -> String {
    sqlx::query("SHOW search_path")
        .fetch_one(&mut **tx)
        .await
        .expect("SHOW search_path")
        .get(0)
}

async fn insert_tenant(pool: &sqlx::PgPool, id: Uuid, strategy: &str, schema_name: Option<&str>, status: &str) {
    sqlx::query(
        "INSERT INTO control.tenants (id, tier, strategy, schema_name, status) VALUES ($1, 'trial', $2, $3, $4)",
    )
    .bind(id)
    .bind(strategy)
    .bind(schema_name)
    .bind(status)
    .execute(pool)
    .await
    .expect("insert control.tenants row");
}

async fn insert_dedicated_tenant(pool: &sqlx::PgPool, id: Uuid, dsn_secret_ref: &str) {
    sqlx::query(
        "INSERT INTO control.tenants (id, tier, strategy, dsn_secret_ref, status) \
         VALUES ($1, 'paid', 'dedicated_db', $2, 'active')",
    )
    .bind(id)
    .bind(dsn_secret_ref)
    .execute(pool)
    .await
    .expect("insert control.tenants row");
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn unregistered_tenant_falls_back_to_public_schema() {
    let pool = connect().await;
    let router = router(pool);
    let tenant = TenantId(Uuid::new_v4()); // never inserted into control.tenants

    let mut tx = router.begin(tenant).await.expect("begin");
    assert!(search_path(&mut tx).await.contains("public"));
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn registered_schema_tenant_routes_to_its_schema_and_does_not_leak() {
    let pool = connect().await;
    let tenant_id = Uuid::new_v4();
    insert_tenant(&pool, tenant_id, "schema", Some("tenant_test1"), "active").await;
    sqlx::query("CREATE SCHEMA IF NOT EXISTS tenant_test1")
        .execute(&pool)
        .await
        .expect("create schema");

    let router = router(pool.clone());
    let mut tx = router.begin(TenantId(tenant_id)).await.expect("begin");
    assert!(search_path(&mut tx).await.contains("tenant_test1"));
    tx.commit().await.expect("commit");

    // A transaction opened directly on the same pool, bypassing Router, must NOT see the
    // previous transaction's `SET LOCAL search_path` — proves it was transaction-scoped, not
    // leaked onto the pooled physical connection (the design's "bẫy #1").
    let mut plain_tx = pool.begin().await.expect("begin plain tx");
    assert!(!search_path(&mut plain_tx).await.contains("tenant_test1"));

    sqlx::query("DROP SCHEMA tenant_test1")
        .execute(&pool)
        .await
        .expect("cleanup schema");
    sqlx::query("DELETE FROM control.tenants WHERE id = $1")
        .bind(tenant_id)
        .execute(&pool)
        .await
        .ok();
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn suspended_tenant_is_rejected() {
    let pool = connect().await;
    let tenant_id = Uuid::new_v4();
    insert_tenant(&pool, tenant_id, "schema", Some("public"), "suspended").await;

    let router = router(pool.clone());
    let err = router
        .begin(TenantId(tenant_id))
        .await
        .expect_err("suspended tenant must be rejected");
    assert_eq!(err.downcast_ref::<RouterError>(), Some(&RouterError::TenantSuspended));

    sqlx::query("DELETE FROM control.tenants WHERE id = $1")
        .bind(tenant_id)
        .execute(&pool)
        .await
        .ok();
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn migrating_tenant_is_rejected() {
    let pool = connect().await;
    let tenant_id = Uuid::new_v4();
    insert_tenant(&pool, tenant_id, "schema", Some("public"), "migrating").await;

    let router = router(pool.clone());
    let err = router
        .begin(TenantId(tenant_id))
        .await
        .expect_err("migrating tenant must be rejected");
    assert_eq!(err.downcast_ref::<RouterError>(), Some(&RouterError::TenantMigrating));

    sqlx::query("DELETE FROM control.tenants WHERE id = $1")
        .bind(tenant_id)
        .execute(&pool)
        .await
        .ok();
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn malicious_schema_name_is_rejected_before_use() {
    let pool = connect().await;
    let tenant_id = Uuid::new_v4();
    insert_tenant(
        &pool,
        tenant_id,
        "schema",
        Some("public; DROP TABLE control.tenants;--"),
        "active",
    )
    .await;

    let router = router(pool.clone());
    let err = router
        .begin(TenantId(tenant_id))
        .await
        .expect_err("malicious schema name must be rejected");
    assert!(matches!(
        err.downcast_ref::<RouterError>(),
        Some(RouterError::InvalidSchemaName(_))
    ));

    sqlx::query("DELETE FROM control.tenants WHERE id = $1")
        .bind(tenant_id)
        .execute(&pool)
        .await
        .ok();
}

/// There's no second real Postgres database available in this dev environment, so this points
/// `dsn_secret_ref` at an env var holding the *same* `DATABASE_URL` the rest of this file already
/// connects to — that's enough to exercise the full path (`EnvStore` lookup -> new `PgPool` ->
/// `begin()`), just not a genuinely separate database. `crm-server`/`dev-tools provision-tenant`
/// against a real second database is covered by the plan's manual smoke test, not by this suite.
#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn dedicated_db_tenant_routes_through_secret_store_to_its_own_pool() {
    let pool = connect().await;
    let database_url = std::env::var("DATABASE_URL").expect("DATABASE_URL required for this e2e test");
    let tenant_id = Uuid::new_v4();
    let dsn_secret_ref = format!("METAP_TEST_DEDICATED_DSN_{}", tenant_id.simple());
    std::env::set_var(&dsn_secret_ref, &database_url);
    insert_dedicated_tenant(&pool, tenant_id, &dsn_secret_ref).await;

    let router = router(pool.clone());
    let mut tx = router
        .begin(TenantId(tenant_id))
        .await
        .expect("begin against dedicated pool");
    let one: i32 = sqlx::query_scalar("SELECT 1")
        .fetch_one(&mut *tx)
        .await
        .expect("SELECT 1");
    assert_eq!(one, 1);
    tx.commit().await.expect("commit");

    std::env::remove_var(&dsn_secret_ref);
    sqlx::query("DELETE FROM control.tenants WHERE id = $1")
        .bind(tenant_id)
        .execute(&pool)
        .await
        .ok();
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn dedicated_db_tenant_with_missing_secret_fails_clearly() {
    let pool = connect().await;
    let tenant_id = Uuid::new_v4();
    insert_dedicated_tenant(&pool, tenant_id, "METAP_TEST_DEDICATED_DSN_NEVER_SET").await;

    let router = router(pool.clone());
    let err = router
        .begin(TenantId(tenant_id))
        .await
        .expect_err("missing secret must fail");
    assert!(err.to_string().contains("METAP_TEST_DEDICATED_DSN_NEVER_SET"));

    sqlx::query("DELETE FROM control.tenants WHERE id = $1")
        .bind(tenant_id)
        .execute(&pool)
        .await
        .ok();
}
