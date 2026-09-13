//! E2E test writing a real row via the repo's dev Postgres. `#[ignore]`d — see
//! `metap-query/tests/query_planner_postgres.rs`'s doc comment for the convention (unit tests
//! never touch a DB; this runs explicitly via `cargo test -- --ignored`).

use chrono::Utc;
use metap_audit::{AuditAction, AuditEntry, AuditTrailStore, PostgresAuditTrailStore};
use sqlx::postgres::PgPoolOptions;
use sqlx::{PgPool, Row};
use uuid::Uuid;

async fn connect() -> PgPool {
    let database_url = std::env::var("DATABASE_URL").expect("DATABASE_URL required for this e2e test");
    PgPoolOptions::new().max_connections(2).connect(&database_url).await.unwrap()
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn postgres_store_round_trips_an_entry() {
    let pool = connect().await;
    let store = PostgresAuditTrailStore::new(pool.clone());

    let tenant_id = Uuid::new_v4();
    let record_id = Uuid::new_v4();
    let actor_user_id = Uuid::new_v4();
    let mut diff = metap_audit::JsonObject::new();
    diff.insert(
        "name".to_string(),
        serde_json::json!({ "before": "Alice", "after": "Bob" }),
    );

    let entry = AuditEntry {
        tenant_id,
        entity: "test.widgets".to_string(),
        record_id,
        action: AuditAction::Update,
        transition_action: None,
        actor_user_id: Some(actor_user_id),
        reason: Some("customer requested name change".to_string()),
        diff: diff.clone(),
        version_after: Some(2),
        occurred_at: Utc::now(),
    };

    store.record(tenant_id, entry).await.unwrap();

    let row = sqlx::query(
        "SELECT entity, record_id, action, actor_user_id, reason, diff, version_after \
         FROM metadata.audit_trail_entries WHERE tenant_id = $1 AND record_id = $2",
    )
    .bind(tenant_id)
    .bind(record_id)
    .fetch_one(&pool)
    .await
    .unwrap();

    assert_eq!(row.get::<String, _>("entity"), "test.widgets");
    assert_eq!(row.get::<Uuid, _>("record_id"), record_id);
    assert_eq!(row.get::<String, _>("action"), "update");
    assert_eq!(row.get::<Option<Uuid>, _>("actor_user_id"), Some(actor_user_id));
    assert_eq!(
        row.get::<Option<String>, _>("reason"),
        Some("customer requested name change".to_string())
    );
    assert_eq!(row.get::<serde_json::Value, _>("diff"), serde_json::Value::Object(diff));
    assert_eq!(row.get::<Option<i32>, _>("version_after"), Some(2));

    sqlx::query("DELETE FROM metadata.audit_trail_entries WHERE tenant_id = $1")
        .bind(tenant_id)
        .execute(&pool)
        .await
        .ok();
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn postgres_store_rejects_a_mismatched_tenant_id() {
    let pool = connect().await;
    let store = PostgresAuditTrailStore::new(pool);

    let entry = AuditEntry {
        tenant_id: Uuid::new_v4(),
        entity: "test.widgets".to_string(),
        record_id: Uuid::new_v4(),
        action: AuditAction::Delete,
        transition_action: None,
        actor_user_id: None,
        reason: None,
        diff: metap_audit::JsonObject::new(),
        version_after: None,
        occurred_at: Utc::now(),
    };

    let err = store.record(Uuid::new_v4(), entry).await.unwrap_err();
    assert!(err.to_string().contains("tenant_id"));
}
