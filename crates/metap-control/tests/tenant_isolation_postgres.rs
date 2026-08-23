//! Security regression suite (`testing/security/checklist.md`) — the tenant-isolation invariant
//! `docs/multi-tenant-platform-design.md` §7 #4 calls out explicitly: "`max_connections=1` buộc
//! tái dùng connection, hai request khác tenant không rò." `router_postgres.rs` already proves a
//! *single* registered tenant's `SET LOCAL search_path` doesn't leak past its own transaction;
//! this file adds the invariant's literal shape — a pool pinned to exactly one physical
//! connection, forced to serve two *different* registered tenants back to back, asserting
//! neither's schema is visible to the other. `#[ignore]`d — see
//! `crates/metap-query/tests/query_planner_postgres.rs`'s doc comment for the convention.

use std::sync::Arc;

use metap_control::{EnvStore, PostgresTenantRegistry, RegistryCache, Router, TenantId};
use sqlx::postgres::PgPoolOptions;
use sqlx::Row;
use uuid::Uuid;

async fn connect_single_connection() -> sqlx::PgPool {
    let database_url = std::env::var("DATABASE_URL").expect("DATABASE_URL required for this e2e test");
    PgPoolOptions::new()
        .max_connections(1)
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

async fn insert_schema_tenant(pool: &sqlx::PgPool, id: Uuid, schema_name: &str) {
    sqlx::query(&format!("CREATE SCHEMA IF NOT EXISTS \"{schema_name}\""))
        .execute(pool)
        .await
        .expect("create schema");
    sqlx::query(
        "INSERT INTO control.tenants (id, tier, strategy, schema_name, status) VALUES ($1, 'trial', 'schema', $2, 'active')",
    )
    .bind(id)
    .bind(schema_name)
    .execute(pool)
    .await
    .expect("insert control.tenants row");
}

async fn cleanup(pool: &sqlx::PgPool, id: Uuid, schema_name: &str) {
    sqlx::query(&format!("DROP SCHEMA IF EXISTS \"{schema_name}\" CASCADE"))
        .execute(pool)
        .await
        .ok();
    sqlx::query("DELETE FROM control.tenants WHERE id = $1")
        .bind(id)
        .execute(pool)
        .await
        .ok();
}

/// The literal §7 #4 scenario: a pool that can only ever hand out *one* physical connection,
/// forced to serve tenant A then tenant B sequentially (there is no other way with
/// `max_connections(1)`) — each `Router::begin` must still land the connection on the correct
/// tenant's schema, proving `SET LOCAL search_path` truly resets per-transaction rather than
/// leaking from whichever tenant used the connection last.
#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn single_connection_pool_never_leaks_search_path_between_two_registered_tenants() {
    let pool = connect_single_connection().await;
    let tenant_a = Uuid::new_v4();
    let tenant_b = Uuid::new_v4();
    let schema_a = format!("tenant_isotesta{}", tenant_a.simple());
    let schema_b = format!("tenant_isotestb{}", tenant_b.simple());
    insert_schema_tenant(&pool, tenant_a, &schema_a).await;
    insert_schema_tenant(&pool, tenant_b, &schema_b).await;

    let router = router(pool.clone());

    // Alternate A, B, A, B, A on the one shared connection — not just A-then-B once. A single
    // reset that "happens to work" the first time wouldn't catch a leak that only shows up on
    // repeated reuse of the same physical connection.
    for round in 0..3 {
        let mut tx_a = router.begin(TenantId(tenant_a)).await.expect("begin tenant A");
        let path_a = search_path(&mut tx_a).await;
        assert!(
            path_a.contains(&schema_a),
            "round {round}: tenant A search_path was {path_a:?}"
        );
        assert!(
            !path_a.contains(&schema_b),
            "round {round}: tenant A search_path leaked B's schema: {path_a:?}"
        );
        tx_a.commit().await.expect("commit A");

        let mut tx_b = router.begin(TenantId(tenant_b)).await.expect("begin tenant B");
        let path_b = search_path(&mut tx_b).await;
        assert!(
            path_b.contains(&schema_b),
            "round {round}: tenant B search_path was {path_b:?}"
        );
        assert!(
            !path_b.contains(&schema_a),
            "round {round}: tenant B search_path leaked A's schema: {path_b:?}"
        );
        tx_b.commit().await.expect("commit B");
    }

    cleanup(&pool, tenant_a, &schema_a).await;
    cleanup(&pool, tenant_b, &schema_b).await;
}
