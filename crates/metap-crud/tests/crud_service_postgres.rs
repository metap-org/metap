//! E2E tests running the full `CrudService` lifecycle against the repo's real dev Postgres.
//! `#[ignore]`d — see `metap-query/tests/query_planner_postgres.rs`'s doc comment for the
//! convention (unit tests never touch a DB; these run explicitly via
//! `cargo test -- --ignored`). This is the integration point for every crate built in
//! Migration Order steps 3–6 — the most important place for real, not just unit-tested,
//! confidence.

use metap_crud::{CrudService, JsonObject, ServiceResult};
use metap_metadata::{
    EntityDefinition, EntityField, EntityWorkflow, FieldKind, MetadataRegistry, WorkflowTransition,
};
use metap_permission::{
    ConditionOp, PermissionService, PolicyCondition, PolicyStore, PolicySubject, PolicyValue,
    PostgresPolicyStore, RequestContext,
};
use metap_query::ListInput;
use serde_json::json;
use sqlx::postgres::PgPoolOptions;
use sqlx::PgPool;
use uuid::Uuid;

fn test_entity() -> EntityDefinition {
    EntityDefinition {
        name: "test.orders".to_string(),
        label: "Order".to_string(),
        table_name: "records".to_string(),
        fields: vec![
            EntityField {
                name: "name".to_string(),
                label: "Name".to_string(),
                kind: FieldKind::String,
                required: Some(true),
                indexed: None,
                unique: None,
                enum_values: None,
                ref_entity: None,
                ref_display_field: None,
                searchable: None,
                search_mode: None,
                sortable: Some(true),
            },
            EntityField {
                name: "amount".to_string(),
                label: "Amount".to_string(),
                kind: FieldKind::Number,
                required: None,
                indexed: None,
                unique: None,
                enum_values: None,
                ref_entity: None,
                ref_display_field: None,
                searchable: None,
                search_mode: None,
                sortable: None,
            },
            EntityField {
                name: "status".to_string(),
                label: "Status".to_string(),
                kind: FieldKind::Enum,
                required: None,
                indexed: None,
                unique: None,
                enum_values: Some(vec!["draft".to_string(), "approved".to_string()]),
                ref_entity: None,
                ref_display_field: None,
                searchable: None,
                search_mode: None,
                sortable: None,
            },
        ],
        list_views: vec![metap_metadata::EntityListView {
            name: "default".to_string(),
            label: "Default".to_string(),
            fields: vec!["name".to_string(), "status".to_string()],
            filters: vec!["status".to_string()],
            default_sort: Some("-createdAt".to_string()),
            max_limit: 50,
        }],
        workflow: Some(EntityWorkflow {
            state_field: "status".to_string(),
            initial_state: "draft".to_string(),
            terminal_states: vec!["approved".to_string()],
            transitions: vec![WorkflowTransition {
                action: "approve".to_string(),
                from: "draft".to_string(),
                to: "approved".to_string(),
                label: "Approve".to_string(),
                guard: Some(PolicyCondition::Attribute {
                    attribute: "amount".to_string(),
                    op: ConditionOp::Eq,
                    value: PolicyValue::Literal { literal: json!(100) },
                }),
            }],
        }),
    }
}

async fn connect() -> PgPool {
    let database_url =
        std::env::var("DATABASE_URL").expect("DATABASE_URL required for this e2e test");
    PgPoolOptions::new().max_connections(5).connect(&database_url).await.unwrap()
}

fn admin_context(tenant_id: Uuid) -> RequestContext {
    RequestContext {
        tenant_id: tenant_id.to_string(),
        user_id: Some(Uuid::new_v4().to_string()),
        roles: Some(vec!["admin".to_string()]),
        function_id: None,
    }
}

async fn cleanup(pool: &PgPool, tenant_id: Uuid) {
    sqlx::query("DELETE FROM outbox_events WHERE aggregate_id IN (SELECT id FROM records WHERE tenant_id = $1)")
        .bind(tenant_id)
        .execute(pool)
        .await
        .ok();
    sqlx::query("DELETE FROM workflow_events WHERE tenant_id = $1")
        .bind(tenant_id)
        .execute(pool)
        .await
        .ok();
    sqlx::query("DELETE FROM records WHERE tenant_id = $1").bind(tenant_id).execute(pool).await.ok();
    sqlx::query("DELETE FROM policies WHERE tenant_id = $1").bind(tenant_id).execute(pool).await.ok();
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn full_lifecycle_create_get_update_transition_delete() {
    let pool = connect().await;
    let tenant_id = Uuid::new_v4();
    let ctx = admin_context(tenant_id);

    let mut registry = MetadataRegistry::new();
    registry.register(test_entity()).unwrap();
    let permissions = PermissionService::new(Box::new(PostgresPolicyStore::new(pool.clone())));
    let crud = CrudService::new(pool.clone(), std::sync::Arc::new(registry), std::sync::Arc::new(permissions));

    // create
    let mut payload = JsonObject::new();
    payload.insert("name".to_string(), json!("First order"));
    payload.insert("amount".to_string(), json!(50));
    let created = match crud.create("test.orders", &payload, &ctx).await.unwrap() {
        ServiceResult::Ok { data, .. } => data,
        other => panic!("expected create to succeed, got {other:?}"),
    };
    assert_eq!(created.status.as_deref(), Some("draft"), "getInitialStatus must set draft");
    assert_eq!(created.version, 1);

    // create validation failure: missing required "name"
    let mut bad_payload = JsonObject::new();
    bad_payload.insert("amount".to_string(), json!(1));
    match crud.create("test.orders", &bad_payload, &ctx).await.unwrap() {
        ServiceResult::Err { status, error, field_errors, .. } => {
            assert_eq!(status, 400);
            assert_eq!(error, "validation_failed");
            assert!(field_errors.unwrap().contains_key("name"));
        }
        other => panic!("expected validation failure, got {other:?}"),
    }

    // get
    let (fetched, capabilities) = match crud.get("test.orders", created.id, &ctx).await.unwrap() {
        ServiceResult::Ok { data, .. } => data,
        other => panic!("expected get to succeed, got {other:?}"),
    };
    assert_eq!(fetched.id, created.id);
    assert!(capabilities.can_update);
    assert_eq!(capabilities.transitions.len(), 1);
    assert_eq!(capabilities.transitions[0].action, "approve");
    assert!(!capabilities.transitions[0].available, "guard requires amount == 100, current is 50");

    // update with stale version -> 409
    let mut update_payload = JsonObject::new();
    update_payload.insert("amount".to_string(), json!(100));
    match crud.update("test.orders", created.id, 999, &update_payload, &ctx).await.unwrap() {
        ServiceResult::Err { status, error, .. } => {
            assert_eq!(status, 409);
            assert_eq!(error, "version_conflict");
        }
        other => panic!("expected version_conflict, got {other:?}"),
    }

    // update with correct version -> succeeds, version increments
    let updated = match crud.update("test.orders", created.id, created.version, &update_payload, &ctx).await.unwrap() {
        ServiceResult::Ok { data, .. } => data,
        other => panic!("expected update to succeed, got {other:?}"),
    };
    assert_eq!(updated.version, 2);
    assert_eq!(updated.data["amount"], json!(100));
    assert_eq!(
        updated.data["status"], json!("draft"),
        "status field must not change via update, only via transition"
    );

    // transition guard now passes (amount == 100)
    let transitioned =
        match crud.transition("test.orders", created.id, "approve", updated.version, &ctx).await.unwrap() {
            ServiceResult::Ok { data, .. } => data,
            other => panic!("expected transition to succeed, got {other:?}"),
        };
    assert_eq!(transitioned.status.as_deref(), Some("approved"));
    assert_eq!(transitioned.data["status"], json!("approved"));
    assert_eq!(transitioned.version, 3);

    // transition again from a now-invalid from-state -> invalid_transition
    match crud.transition("test.orders", created.id, "approve", transitioned.version, &ctx).await.unwrap() {
        ServiceResult::Err { status, error, .. } => {
            assert_eq!(status, 409);
            assert_eq!(error, "invalid_transition");
        }
        other => panic!("expected invalid_transition, got {other:?}"),
    }

    // delete (soft)
    let deleted = match crud.delete("test.orders", created.id, transitioned.version, &ctx).await.unwrap() {
        ServiceResult::Ok { data, .. } => data,
        other => panic!("expected delete to succeed, got {other:?}"),
    };
    assert_eq!(deleted.version, 4);

    // get after delete -> 404 (soft-deleted rows are excluded)
    match crud.get("test.orders", created.id, &ctx).await.unwrap() {
        ServiceResult::Err { status, error, .. } => {
            assert_eq!(status, 404);
            assert_eq!(error, "record_not_found");
        }
        other => panic!("expected record_not_found after delete, got {other:?}"),
    }

    // workflow_events audit trail has exactly one row (the one real transition)
    let event_count: i64 = sqlx::query_scalar(
        "SELECT count(*) FROM workflow_events WHERE tenant_id = $1 AND record_id = $2",
    )
    .bind(tenant_id)
    .bind(created.id)
    .fetch_one(&pool)
    .await
    .unwrap();
    assert_eq!(event_count, 1);

    // outbox got: record.created, record.updated, workflow.transitioned, record.deleted
    let topics: Vec<String> = sqlx::query_scalar(
        "SELECT topic FROM outbox_events WHERE aggregate_id = $1 ORDER BY created_at",
    )
    .bind(created.id)
    .fetch_all(&pool)
    .await
    .unwrap();
    assert_eq!(
        topics,
        vec![
            "test.orders.record.created",
            "test.orders.record.updated",
            "test.orders.workflow.transitioned",
            "test.orders.record.deleted",
        ]
    );

    cleanup(&pool, tenant_id).await;
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn list_returns_created_records_scoped_to_tenant() {
    let pool = connect().await;
    let tenant_id = Uuid::new_v4();
    let ctx = admin_context(tenant_id);

    let mut registry = MetadataRegistry::new();
    registry.register(test_entity()).unwrap();
    let permissions = PermissionService::new(Box::new(PostgresPolicyStore::new(pool.clone())));
    let crud = CrudService::new(pool.clone(), std::sync::Arc::new(registry), std::sync::Arc::new(permissions));

    for name in ["a", "b", "c"] {
        let mut payload = JsonObject::new();
        payload.insert("name".to_string(), json!(name));
        crud.create("test.orders", &payload, &ctx).await.unwrap();
    }

    let input = ListInput { limit: 50, ..Default::default() };
    let list_result = crud.list("test.orders", &input, &ctx).await.unwrap();
    match list_result {
        ServiceResult::Ok { data, page } => {
            assert_eq!(data.len(), 3);
            assert!(page.is_some());
        }
        other => panic!("expected list to succeed, got {other:?}"),
    }

    cleanup(&pool, tenant_id).await;
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn non_admin_field_write_policy_is_enforced_through_create() {
    let pool = connect().await;
    let tenant_id = Uuid::new_v4();

    let mut registry = MetadataRegistry::new();
    registry.register(test_entity()).unwrap();
    let store = PostgresPolicyStore::new(pool.clone());

    // A "write" policy on "amount" that only sales can write, with no condition —
    // exercises the real PostgresPolicyStore -> PermissionSnapshot -> assertWritableFields
    // path through CrudService.create, not just the pure logic in isolation.
    store
        .create_policy(
            tenant_id,
            "test.orders",
            "write",
            Some(vec!["sales".to_string()]),
            None,
            None,
            Some("amount"),
            Some(PolicySubject::Context),
        )
        .await
        .unwrap();

    let permissions = PermissionService::new(Box::new(store));
    let crud = CrudService::new(pool.clone(), std::sync::Arc::new(registry), std::sync::Arc::new(permissions));

    let ctx = RequestContext {
        tenant_id: tenant_id.to_string(),
        user_id: Some(Uuid::new_v4().to_string()),
        roles: Some(vec!["support".to_string()]), // not "sales" — must be denied on "amount"
        function_id: None,
    };

    let mut payload = JsonObject::new();
    payload.insert("name".to_string(), json!("blocked"));
    payload.insert("amount".to_string(), json!(1));

    match crud.create("test.orders", &payload, &ctx).await.unwrap() {
        ServiceResult::Err { status, field_errors, .. } => {
            assert_eq!(status, 403);
            assert!(field_errors.unwrap().contains_key("amount"));
        }
        other => panic!("expected a field-level 403 on amount, got {other:?}"),
    }

    cleanup(&pool, tenant_id).await;
}
