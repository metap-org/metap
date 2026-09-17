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
    insert_tenant(&pool, tenant_id, "schema", Some("t_test1"), "active").await;
    sqlx::query("CREATE SCHEMA IF NOT EXISTS t_test1")
        .execute(&pool)
        .await
        .expect("create schema");

    let router = router(pool.clone());
    let mut tx = router.begin(TenantId(tenant_id)).await.expect("begin");
    assert!(search_path(&mut tx).await.contains("t_test1"));
    tx.commit().await.expect("commit");

    // A transaction opened directly on the same pool, bypassing Router, must NOT see the
    // previous transaction's `SET LOCAL search_path` — proves it was transaction-scoped, not
    // leaked onto the pooled physical connection (the design's "bẫy #1").
    let mut plain_tx = pool.begin().await.expect("begin plain tx");
    assert!(!search_path(&mut plain_tx).await.contains("t_test1"));

    sqlx::query("DROP SCHEMA t_test1")
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

/// Regression test for audit 06 finding #1: `pool_for` used to validate a `Schema` tenant's
/// `schema_name` and then return the shared pool anyway, on the (by then false) premise that
/// `schema_name` is always `"public"`. Once `provision_schema_tenant` started generating
/// `t_<uuid>` names, that meant `begin()` and `pool_for()` resolved **different physical
/// schemas for the same tenant** — `CrudService` wrote one, every `pool_for` caller
/// (`../metap-lowcode`'s `presenter::resolve_pool` runs on every one of its HTTP handlers, and
/// its `reconciler-orchestrator` runs tenant DDL) read and wrote the other.
///
/// Writes through `begin()` and reads back, unqualified, through `pool_for()`: the two must
/// agree. Also asserts the shared pool still does *not* see the row, so this is proving real
/// schema scoping rather than everything having quietly landed in one shared table.
#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn pool_for_resolves_the_same_schema_begin_does() {
    let pool = connect().await;
    let tenant_id = Uuid::new_v4();
    let schema = format!("t_poolfor{}", &tenant_id.simple().to_string()[..8]);
    insert_tenant(&pool, tenant_id, "schema", Some(&schema), "active").await;
    sqlx::query(&format!("CREATE SCHEMA IF NOT EXISTS \"{schema}\""))
        .execute(&pool)
        .await
        .expect("create schema");
    sqlx::query(&format!(
        "CREATE TABLE \"{schema}\".policies (LIKE metadata.policies INCLUDING DEFAULTS INCLUDING CONSTRAINTS)"
    ))
    .execute(&pool)
    .await
    .expect("clone policies into the tenant schema");

    let router = router(pool.clone());

    let mut tx = router.begin(TenantId(tenant_id)).await.expect("begin");
    sqlx::query(
        "INSERT INTO policies (id, tenant_id, entity, action, effect) \
         VALUES (gen_random_uuid(), $1, 'audit06', 'read', 'allow')",
    )
    .bind(tenant_id)
    .execute(&mut *tx)
    .await
    .expect("insert through begin()");
    tx.commit().await.expect("commit");

    let tenant_pool = router.pool_for(TenantId(tenant_id)).await.expect("pool_for");
    let seen: i64 = sqlx::query_scalar("SELECT count(*) FROM policies WHERE entity = 'audit06'")
        .fetch_one(&tenant_pool)
        .await
        .expect("count through pool_for()");
    assert_eq!(
        seen, 1,
        "pool_for must resolve the same schema begin() wrote to — got 0, meaning it is still \
         handing back the shared pool with the default search_path"
    );

    let shared_seen: i64 = sqlx::query_scalar("SELECT count(*) FROM policies WHERE entity = 'audit06'")
        .fetch_one(&pool)
        .await
        .expect("count through the shared pool");
    assert_eq!(
        shared_seen, 0,
        "the row must live in the tenant's own schema, not the shared metadata.policies"
    );

    sqlx::query(&format!("DROP SCHEMA \"{schema}\" CASCADE"))
        .execute(&pool)
        .await
        .ok();
    sqlx::query("DELETE FROM control.tenants WHERE id = $1")
        .bind(tenant_id)
        .execute(&pool)
        .await
        .ok();
}
