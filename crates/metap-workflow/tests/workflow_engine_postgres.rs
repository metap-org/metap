//! E2E tests writing real rows via the repo's dev Postgres. `#[ignore]`d — see
//! `metap-query/tests/query_planner_postgres.rs`'s doc comment for the convention (unit
//! tests never touch a DB; these run explicitly via `cargo test -- --ignored`).

use metap_metadata::EntityDefinition;
use metap_permission::RequestContext;
use metap_workflow::{emit_created, emit_transitioned, record_event, JsonObject};
use serde_json::json;
use sqlx::postgres::PgPoolOptions;
use sqlx::{PgPool, Row};
use uuid::Uuid;

fn entity() -> EntityDefinition {
    EntityDefinition {
        name: "test.widgets".to_string(),
        label: "Widget".to_string(),
        table_name: "records".to_string(),
        fields: vec![],
        list_views: vec![],
        workflow: None,
    }
}

async fn connect() -> PgPool {
    let database_url =
        std::env::var("DATABASE_URL").expect("DATABASE_URL required for this e2e test");
    PgPoolOptions::new().max_connections(2).connect(&database_url).await.unwrap()
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn record_event_writes_an_append_only_audit_row() {
    let pool = connect().await;
    let entity = entity();
    let record_id = Uuid::new_v4();
    let ctx = RequestContext {
        tenant_id: Uuid::new_v4().to_string(),
        user_id: Some(Uuid::new_v4().to_string()),
        roles: None,
        function_id: None,
    };

    record_event(&pool, &entity, record_id, "activate", "draft", "active", &ctx)
        .await
        .expect("record_event");

    let row = sqlx::query(
        "SELECT entity, record_id, action, from_state, to_state FROM workflow_events \
         WHERE record_id = $1",
    )
    .bind(record_id)
    .fetch_one(&pool)
    .await
    .expect("row must exist");

    assert_eq!(row.get::<String, _>("entity"), "test.widgets");
    assert_eq!(row.get::<String, _>("action"), "activate");
    assert_eq!(row.get::<String, _>("from_state"), "draft");
    assert_eq!(row.get::<String, _>("to_state"), "active");

    sqlx::query("DELETE FROM workflow_events WHERE record_id = $1")
        .bind(record_id)
        .execute(&pool)
        .await
        .ok();
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn emit_transitioned_and_emit_created_enqueue_outbox_rows_with_correct_topics() {
    let pool = connect().await;
    let entity = entity();
    let record_id = Uuid::new_v4();

    emit_transitioned(&pool, &entity, record_id, "activate", "draft", "active", None)
        .await
        .expect("emit_transitioned");

    let mut data = JsonObject::new();
    data.insert("name".to_string(), json!("Acme"));
    emit_created(&pool, &entity, record_id, &data).await.expect("emit_created");

    let rows = sqlx::query("SELECT topic, aggregate_id, payload FROM outbox_events WHERE aggregate_id = $1 ORDER BY created_at")
        .bind(record_id)
        .fetch_all(&pool)
        .await
        .expect("fetch outbox rows");

    assert_eq!(rows.len(), 2);
    assert_eq!(rows[0].get::<String, _>("topic"), "test.widgets.workflow.transitioned");
    assert_eq!(rows[1].get::<String, _>("topic"), "test.widgets.record.created");
    let created_payload: serde_json::Value = rows[1].get("payload");
    assert_eq!(created_payload["data"]["name"], "Acme");

    sqlx::query("DELETE FROM outbox_events WHERE aggregate_id = $1")
        .bind(record_id)
        .execute(&pool)
        .await
        .ok();
}
