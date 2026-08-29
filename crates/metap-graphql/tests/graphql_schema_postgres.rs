//! E2E test: a real dynamic schema, built from a real `MetadataRegistry`, executed against a
//! real Postgres through the real `CrudService`/`PermissionService` pipeline. `#[ignore]`d, same
//! convention as `crates/metap-crud/tests/crud_service_postgres.rs` (needs a running Postgres,
//! run explicitly via `cargo test -- --ignored`).

use std::sync::Arc;

use arc_swap::ArcSwap;
use metap_control::PostgresPolicyStore;
use metap_crud::CrudService;
use metap_graphql::{build_schema, with_request_data, SchemaLimits};
use metap_metadata::{
    EntityDefinition, EntityField, EntityListView, EntityWorkflow, FieldKind, MetadataRegistry, WorkflowTransition,
};
use metap_permission::{PermissionService, PolicyEffect, PolicyStore, PolicySubject, RequestContext};
use serde_json::json;
use sqlx::postgres::PgPoolOptions;
use sqlx::PgPool;
use uuid::Uuid;

fn test_router(pool: PgPool) -> metap_control::Router {
    let registry = Arc::new(metap_control::PostgresTenantRegistry::new(pool.clone()));
    metap_control::Router::new(
        pool,
        metap_control::RegistryCache::new(registry),
        Arc::new(metap_control::EnvStore),
    )
}

fn admin_context(tenant_id: Uuid) -> RequestContext {
    RequestContext {
        tenant_id: tenant_id.to_string(),
        user_id: Some(Uuid::new_v4().to_string()),
        roles: Some(vec!["admin".to_string()]),
        function_id: None,
        context_attributes: None,
    }
}

async fn connect() -> PgPool {
    let database_url = std::env::var("DATABASE_URL").expect("DATABASE_URL required for this e2e test");
    PgPoolOptions::new()
        .max_connections(5)
        .connect(&database_url)
        .await
        .unwrap()
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
    sqlx::query("DELETE FROM records WHERE tenant_id = $1")
        .bind(tenant_id)
        .execute(pool)
        .await
        .ok();
    sqlx::query("DELETE FROM policies WHERE tenant_id = $1")
        .bind(tenant_id)
        .execute(pool)
        .await
        .ok();
}

fn string_field(name: &str) -> EntityField {
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
        searchable: None,
        search_mode: None,
        sortable: None,
        storage: None,
        min: None,
        max: None,
        min_length: None,
        max_length: None,
    }
}

fn parent_entity() -> EntityDefinition {
    EntityDefinition {
        name: "test.gql_parents".to_string(),
        label: "Parent".to_string(),
        table_name: "records".to_string(),
        fields: vec![string_field("name"), string_field("secret")],
        list_views: vec![EntityListView {
            name: "default".to_string(),
            label: "Default".to_string(),
            fields: vec!["name".to_string()],
            filters: vec![],
            default_sort: None,
            max_limit: 50,
        }],
        workflow: None,
    }
}

fn child_entity() -> EntityDefinition {
    EntityDefinition {
        name: "test.gql_children".to_string(),
        label: "Child".to_string(),
        table_name: "records".to_string(),
        fields: vec![EntityField {
            name: "parentId".to_string(),
            label: "Parent".to_string(),
            kind: FieldKind::Reference,
            required: None,
            indexed: None,
            unique: None,
            enum_values: None,
            ref_entity: Some("test.gql_parents".to_string()),
            ref_display_field: None,
            searchable: None,
            search_mode: None,
            sortable: None,
            storage: None,
            min: None,
            max: None,
            min_length: None,
            max_length: None,
        }],
        list_views: vec![EntityListView {
            name: "default".to_string(),
            label: "Default".to_string(),
            fields: vec![],
            filters: vec![],
            default_sort: None,
            max_limit: 50,
        }],
        workflow: None,
    }
}

fn workflow_entity() -> EntityDefinition {
    EntityDefinition {
        name: "test.gql_orders".to_string(),
        label: "Order".to_string(),
        table_name: "records".to_string(),
        fields: vec![
            string_field("name"),
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
                storage: None,
                min: None,
                max: None,
                min_length: None,
                max_length: None,
            },
        ],
        list_views: vec![EntityListView {
            name: "default".to_string(),
            label: "Default".to_string(),
            fields: vec![],
            filters: vec![],
            default_sort: None,
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
                guard: None,
                validator: None,
                set_fields: None,
            }],
        }),
    }
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn full_graphql_lifecycle_reference_expansion_and_field_masking() {
    let pool = connect().await;
    let tenant_id = Uuid::new_v4();

    let mut registry = MetadataRegistry::new();
    registry.register(parent_entity()).unwrap();
    registry.register(child_entity()).unwrap();
    registry.register(workflow_entity()).unwrap();

    let store = PostgresPolicyStore::new(test_router(pool.clone()));
    // Field-level read policy: "viewer" can't read `test.gql_parents.secret`.
    store
        .create_policy(
            tenant_id,
            "test.gql_parents",
            "read",
            Some(vec!["viewer".to_string()]),
            None,
            None,
            None,
            Some(PolicySubject::Context),
            PolicyEffect::Allow,
        )
        .await
        .unwrap();
    store
        .create_policy(
            tenant_id,
            "test.gql_children",
            "read",
            Some(vec!["viewer".to_string()]),
            None,
            None,
            None,
            Some(PolicySubject::Context),
            PolicyEffect::Allow,
        )
        .await
        .unwrap();
    store
        .create_policy(
            tenant_id,
            "test.gql_parents",
            "read",
            None, // every role, including "viewer" — field policies aren't role-gated here
            None,
            None,
            Some("secret"),
            Some(PolicySubject::Context),
            PolicyEffect::Deny,
        )
        .await
        .unwrap();

    let permissions = Arc::new(PermissionService::new(Box::new(store)));
    let crud = Arc::new(CrudService::new(
        test_router(pool.clone()),
        Arc::new(ArcSwap::new(Arc::new(registry.clone()))),
        permissions,
    ));

    let admin_ctx = admin_context(tenant_id);

    // Seed a parent + two children referencing it, as the admin (bypasses the field-deny policy
    // above, so the parent's `secret` is actually written).
    let mut parent_payload = metap_crud::JsonObject::new();
    parent_payload.insert("name".to_string(), json!("Acme"));
    parent_payload.insert("secret".to_string(), json!("classified"));
    let parent = match crud
        .create("test.gql_parents", &parent_payload, &admin_ctx)
        .await
        .unwrap()
    {
        metap_crud::ServiceResult::Ok { data, .. } => data,
        other => panic!("expected parent create to succeed, got {other:?}"),
    };

    for _ in 0..2 {
        let mut child_payload = metap_crud::JsonObject::new();
        child_payload.insert("parentId".to_string(), json!(parent.id.to_string()));
        crud.create("test.gql_children", &child_payload, &admin_ctx)
            .await
            .unwrap();
    }

    let schema = build_schema(&registry, crud.clone(), SchemaLimits::default()).unwrap();

    // Query as "viewer": list children, expand each `parentId` Reference field into the full
    // Parent object — proves the DataLoader wiring resolves nested references at all — and
    // request the denied `secret` field, which must come back `null`, not an error.
    let viewer_ctx = RequestContext {
        tenant_id: tenant_id.to_string(),
        user_id: Some(Uuid::new_v4().to_string()),
        roles: Some(vec!["viewer".to_string()]),
        function_id: None,
        context_attributes: None,
    };
    let query = r#"
        {
            testGqlChildrenList {
                records {
                    id
                    parentId { name secret }
                }
            }
        }
    "#;
    let request = with_request_data(async_graphql::Request::new(query), crud.clone(), viewer_ctx);
    let response = schema.execute(request).await;
    assert!(
        response.errors.is_empty(),
        "unexpected GraphQL errors: {:?}",
        response.errors
    );
    let data = response.data.into_json().unwrap();
    let records = data["testGqlChildrenList"]["records"].as_array().unwrap();
    assert_eq!(records.len(), 2);
    for record in records {
        assert_eq!(record["parentId"]["name"], "Acme");
        assert_eq!(
            record["parentId"]["secret"],
            serde_json::Value::Null,
            "field-level deny must mask `secret` to null, not surface it"
        );
    }

    // Mutation lifecycle (create/get/transition/delete) as admin, against the workflow entity.
    let admin_query_ctx = admin_ctx.clone();
    let create_mutation = r#"
        mutation {
            createTestGqlOrders(data: { name: "First" }) { id version status }
        }
    "#;
    let request = with_request_data(
        async_graphql::Request::new(create_mutation),
        crud.clone(),
        admin_query_ctx.clone(),
    );
    let response = schema.execute(request).await;
    assert!(
        response.errors.is_empty(),
        "create mutation failed: {:?}",
        response.errors
    );
    let created = response.data.into_json().unwrap();
    let order_id = created["createTestGqlOrders"]["id"].as_str().unwrap().to_string();
    assert_eq!(created["createTestGqlOrders"]["status"], "draft");
    let version = created["createTestGqlOrders"]["version"].as_i64().unwrap();

    let transition_mutation = format!(
        r#"mutation {{ transitionTestGqlOrders(id: "{order_id}", action: "approve", expectedVersion: {version}) {{ status }} }}"#
    );
    let request = with_request_data(
        async_graphql::Request::new(transition_mutation),
        crud.clone(),
        admin_query_ctx.clone(),
    );
    let response = schema.execute(request).await;
    assert!(
        response.errors.is_empty(),
        "transition mutation failed: {:?}",
        response.errors
    );
    let transitioned = response.data.into_json().unwrap();
    assert_eq!(transitioned["transitionTestGqlOrders"]["status"], "approved");

    let get_query = format!(r#"{{ testGqlOrders(id: "{order_id}") {{ status }} }}"#);
    let request = with_request_data(async_graphql::Request::new(get_query), crud.clone(), admin_query_ctx);
    let response = schema.execute(request).await;
    assert!(response.errors.is_empty(), "get query failed: {:?}", response.errors);
    let fetched = response.data.into_json().unwrap();
    assert_eq!(fetched["testGqlOrders"]["status"], "approved");

    cleanup(&pool, tenant_id).await;
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn overly_deep_query_is_rejected_by_the_depth_limit() {
    let pool = connect().await;
    let tenant_id = Uuid::new_v4();

    let mut registry = MetadataRegistry::new();
    registry.register(parent_entity()).unwrap();
    registry.register(child_entity()).unwrap();

    let permissions = Arc::new(PermissionService::new(Box::new(PostgresPolicyStore::new(test_router(
        pool.clone(),
    )))));
    let crud = Arc::new(CrudService::new(
        test_router(pool.clone()),
        Arc::new(ArcSwap::new(Arc::new(registry.clone()))),
        permissions,
    ));

    // depth=2 is enough for `{ testGqlChildrenList { records { id } } }` (Query -> Connection ->
    // records -> id is 3 levels of *fields*, async-graphql's depth counts selection nesting) but
    // not for a query that nests one level deeper via the `parentId` expansion.
    let schema = build_schema(
        &registry,
        crud.clone(),
        SchemaLimits {
            depth: 2,
            complexity: 1000,
        },
    )
    .unwrap();

    let ctx = admin_context(tenant_id);
    let query = r#"
        {
            testGqlChildrenList {
                records {
                    id
                    parentId { name }
                }
            }
        }
    "#;
    let request = with_request_data(async_graphql::Request::new(query), crud.clone(), ctx);
    let response = schema.execute(request).await;
    assert!(
        !response.errors.is_empty(),
        "expected the depth limit to reject this query"
    );
    assert!(
        response
            .errors
            .iter()
            .any(|e| e.message.to_lowercase().contains("deep")),
        "expected a depth-limit error, got {:?}",
        response.errors
    );

    cleanup(&pool, tenant_id).await;
}
