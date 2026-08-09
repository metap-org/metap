//! E2E tests against the repo's real dev Postgres. `#[ignore]`d — see
//! `metap-query/tests/query_planner_postgres.rs`'s doc comment for the convention.

use metap_metadata::{EntityField, EntityListView, EntitySummary, FieldKind};
use metap_peripherals::{
    assign_role, check_metadata_drift, get_roles_for_user, list_users, reconcile_indexes,
    revoke_role,
};
use sqlx::postgres::PgPoolOptions;
use sqlx::{PgPool, Row};
use uuid::Uuid;

async fn connect() -> PgPool {
    let database_url =
        std::env::var("DATABASE_URL").expect("DATABASE_URL required for this e2e test");
    PgPoolOptions::new().max_connections(3).connect(&database_url).await.unwrap()
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn role_assignment_round_trip() {
    let pool = connect().await;
    let tenant_id = Uuid::new_v4();
    let user_id = Uuid::new_v4();

    assert!(get_roles_for_user(&pool, tenant_id, user_id).await.unwrap().is_empty());

    assign_role(&pool, tenant_id, user_id, "sales", None).await.unwrap();
    assign_role(&pool, tenant_id, user_id, "support", None).await.unwrap();
    // duplicate assign is a no-op, not an error (ON CONFLICT DO NOTHING)
    assign_role(&pool, tenant_id, user_id, "sales", None).await.unwrap();

    let mut roles = get_roles_for_user(&pool, tenant_id, user_id).await.unwrap();
    roles.sort();
    assert_eq!(roles, vec!["sales".to_string(), "support".to_string()]);

    let users = list_users(&pool, tenant_id).await.unwrap();
    assert_eq!(users.len(), 1);
    assert_eq!(users[0].user_id, user_id);

    revoke_role(&pool, tenant_id, user_id, "sales").await.unwrap();
    let roles = get_roles_for_user(&pool, tenant_id, user_id).await.unwrap();
    assert_eq!(roles, vec!["support".to_string()]);

    sqlx::query("DELETE FROM user_roles WHERE tenant_id = $1").bind(tenant_id).execute(&pool).await.ok();
}

fn summary(name: &str, version: &str) -> EntitySummary {
    EntitySummary {
        name: name.to_string(),
        label: "Test".to_string(),
        fields: vec![],
        list_views: vec![],
        workflow: None,
        version: version.to_string(),
    }
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn metadata_drift_records_first_boot_then_detects_change() {
    let pool = connect().await;
    let entity_name = format!("test.drift.{}", Uuid::new_v4());

    check_metadata_drift(&pool, &[summary(&entity_name, "hash-v1")]).await;
    let stored: String =
        sqlx::query_scalar("SELECT hash FROM metadata_versions WHERE entity_name = $1")
            .bind(&entity_name)
            .fetch_one(&pool)
            .await
            .unwrap();
    assert_eq!(stored, "hash-v1");

    // second call with a different hash — drift is logged (stderr), row is updated either way
    check_metadata_drift(&pool, &[summary(&entity_name, "hash-v2")]).await;
    let stored: String =
        sqlx::query_scalar("SELECT hash FROM metadata_versions WHERE entity_name = $1")
            .bind(&entity_name)
            .fetch_one(&pool)
            .await
            .unwrap();
    assert_eq!(stored, "hash-v2");

    sqlx::query("DELETE FROM metadata_versions WHERE entity_name = $1")
        .bind(&entity_name)
        .execute(&pool)
        .await
        .ok();
}

fn indexed_field(name: &str) -> EntityField {
    EntityField {
        name: name.to_string(),
        label: name.to_string(),
        kind: FieldKind::String,
        required: None,
        indexed: Some(true),
        unique: None,
        enum_values: None,
        ref_entity: None,
        ref_display_field: None,
        searchable: None,
        search_mode: None,
        sortable: None,
    }
}

fn fts_field(name: &str) -> EntityField {
    EntityField {
        name: name.to_string(),
        label: name.to_string(),
        kind: FieldKind::String,
        required: None,
        indexed: None,
        unique: None,
        enum_values: None,
        ref_entity: None,
        ref_display_field: None,
        searchable: Some(true),
        search_mode: Some("fts".to_string()),
        sortable: None,
    }
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn reconcile_creates_an_index_postgres_actually_selects_for_the_exact_query_form() {
    let pool = connect().await;
    // Short suffix, not a full UUID — a realistic entity name length (e.g. "crm.customers"),
    // since `gin_records_<entity>_<field>`/`idx_records_<entity>_<field>` must stay under
    // Postgres's 63-byte identifier limit or the actual stored name silently truncates,
    // which is a test-fixture concern, not something `index_reconciler.rs` needs to solve.
    let entity_name = format!("test.idx{:x}", Uuid::new_v4().as_u128() % 0xFFFFFF);

    let entity = EntitySummary {
        name: entity_name.clone(),
        label: "Test".to_string(),
        fields: vec![indexed_field("sku")],
        list_views: vec![EntityListView {
            name: "default".to_string(),
            label: "Default".to_string(),
            fields: vec![],
            filters: vec![],
            default_sort: None,
            max_limit: 50,
        }],
        workflow: None,
        version: "v1".to_string(),
    };

    reconcile_indexes(&pool, &[entity]).await;

    let index_name = format!("idx_records_{}_sku", entity_name.replace('.', "_"));
    let exists: Option<i32> = sqlx::query_scalar("SELECT 1 FROM pg_indexes WHERE indexname = $1")
        .bind(&index_name)
        .fetch_optional(&pool)
        .await
        .unwrap();
    assert!(exists.is_some(), "expected index {index_name} to have been created");

    // Same rigor as the original TS test suite: confirm Postgres's planner actually
    // selects this index for the exact expression form QueryPlanner emits
    // (jsonb_extract_path_text), not just that the index exists.
    let explain_rows = sqlx::query(&format!(
        "EXPLAIN SELECT id FROM records WHERE entity = '{entity_name}' AND deleted = false \
         AND jsonb_extract_path_text(data, 'sku') = 'X'"
    ))
    .fetch_all(&pool)
    .await
    .unwrap();
    let plan: String = explain_rows.iter().map(|r| r.get::<String, _>(0)).collect::<Vec<_>>().join("\n");
    assert!(
        plan.contains(&index_name),
        "expected query plan to use {index_name}, got:\n{plan}"
    );

    sqlx::query(&format!("DROP INDEX CONCURRENTLY IF EXISTS {index_name}")).execute(&pool).await.ok();

    // idempotent: reconciling again when the index already exists is a no-op, not an error
    let entity_again = EntitySummary {
        name: entity_name.clone(),
        label: "Test".to_string(),
        fields: vec![fts_field("description")],
        list_views: vec![],
        workflow: None,
        version: "v1".to_string(),
    };
    reconcile_indexes(&pool, &[entity_again]).await;
    let gin_index_name = format!("gin_records_{}_description", entity_name.replace('.', "_"));
    let gin_exists: Option<i32> = sqlx::query_scalar("SELECT 1 FROM pg_indexes WHERE indexname = $1")
        .bind(&gin_index_name)
        .fetch_optional(&pool)
        .await
        .unwrap();
    assert!(gin_exists.is_some(), "expected GIN index {gin_index_name} to have been created");
    sqlx::query(&format!("DROP INDEX CONCURRENTLY IF EXISTS {gin_index_name}")).execute(&pool).await.ok();
}
