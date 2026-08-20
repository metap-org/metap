//! E2E test against the repo's real dev Postgres (see `CLAUDE.md`'s Commands section:
//! `docker compose up -d postgres`). `#[ignore]`d so a plain `cargo test` (unit tests only)
//! never touches a database — run these explicitly with `cargo test -- --ignored` (or
//! `--include-ignored`) once the dev DB is up.
//!
//! Moved here from `crates/metap-permission/tests/` (`docs/roadmap.md` Phase 16 gap, closed
//! 2026-08-20) along with `PostgresPolicyStore` itself — the concrete impl now lives in
//! `metap-control` (needs `Router`), so this is where its own e2e coverage belongs; the
//! `PolicyStore` trait it implements still lives in `metap-permission`.

use std::sync::Arc;

use metap_control::{EnvStore, PostgresPolicyStore, PostgresTenantRegistry, RegistryCache, Router};
use metap_permission::{PolicyStore, PolicySubject};
use sqlx::postgres::PgPoolOptions;
use uuid::Uuid;

async fn connect() -> sqlx::PgPool {
    let database_url = std::env::var("DATABASE_URL").expect("DATABASE_URL required for this e2e test");
    PgPoolOptions::new()
        .max_connections(2)
        .connect(&database_url)
        .await
        .expect("connect to dev postgres")
}

fn router(pool: sqlx::PgPool) -> Router {
    let registry = Arc::new(PostgresTenantRegistry::new(pool.clone()));
    Router::new(pool, RegistryCache::new(registry), Arc::new(EnvStore))
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn create_list_and_delete_round_trip_against_real_postgres() {
    let pool = connect().await;
    let store = PostgresPolicyStore::new(router(pool));
    let tenant_id = Uuid::new_v4(); // isolated per test run, nothing to collide with

    let created = store
        .create_policy(
            tenant_id,
            "crm.customers",
            "read",
            Some(vec!["sales".to_string()]),
            None,
            None,
            None,
            Some(PolicySubject::Context),
        )
        .await
        .expect("create_policy");

    assert_eq!(created.entity, "crm.customers");
    assert_eq!(created.action, "read");
    assert_eq!(created.roles.as_deref(), Some(["sales".to_string()].as_slice()));

    let context_policies = store
        .find_context_policies(tenant_id, "crm.customers", "read")
        .await
        .expect("find_context_policies");
    assert_eq!(context_policies.len(), 1);
    assert_eq!(context_policies[0].id, created.id);

    let all = store
        .load_all_policies(tenant_id, "crm.customers")
        .await
        .expect("load_all_policies");
    assert_eq!(all.len(), 1);

    let listed = store.list_policies(tenant_id, None).await.expect("list_policies");
    assert_eq!(listed.len(), 1);

    store.delete_policy(tenant_id, created.id).await.expect("delete_policy");

    let after_delete = store
        .load_all_policies(tenant_id, "crm.customers")
        .await
        .expect("load_all_policies after delete");
    assert!(after_delete.is_empty());
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn create_policy_with_condition_round_trips_jsonb() {
    let pool = connect().await;
    let store = PostgresPolicyStore::new(router(pool));
    let tenant_id = Uuid::new_v4();

    let condition: metap_permission::PolicyCondition = serde_json::from_value(serde_json::json!({
        "attribute": "ownerId",
        "op": "eq",
        "value": { "fromContext": "userId" }
    }))
    .unwrap();

    let created = store
        .create_policy(
            tenant_id,
            "crm.customers",
            "update",
            None,
            Some(condition),
            None,
            None,
            Some(PolicySubject::Record),
        )
        .await
        .expect("create_policy with condition");

    assert!(created.condition.is_some());
    assert_eq!(created.subject, "record");

    let reloaded = store.load_all_policies(tenant_id, "crm.customers").await.unwrap();
    assert_eq!(reloaded.len(), 1);
    assert!(reloaded[0].condition.is_some());

    store.delete_policy(tenant_id, created.id).await.unwrap();
}
