//! E2E test: a real dynamic schema, built from a real `MetadataRegistry`, executed against a
//! real Postgres through the real `CrudService`/`PermissionService` pipeline. `#[ignore]`d, same
//! convention as `crates/metap-crud/tests/crud_service_postgres.rs` (needs a running Postgres,
//! run explicitly via `cargo test -- --ignored`).

use std::sync::Arc;

use arc_swap::ArcSwap;
use metap_control::PostgresPolicyStore;
use metap_crud::CrudService;
use metap_graphql::{build_schema, build_schema_with_federation, with_request_data, SchemaLimits};
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
        forwarded_bearer_token: None,
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

/// Dedicated tables this file creates itself (`ensure_tables`, `CREATE TABLE IF NOT EXISTS`) —
/// the shared `records` table these used to point at no longer exists at all
/// (`crates/migrations/0033_drop_records_table.sql`).
const PARENT_TABLE: &str = "entities.test_gql_parents";
const CHILD_TABLE: &str = "entities.test_gql_children";
const ORDERS_TABLE: &str = "entities.test_gql_orders";

async fn ensure_tables(pool: &PgPool) {
    sqlx::query("CREATE SCHEMA IF NOT EXISTS entities")
        .execute(pool)
        .await
        .unwrap();
    for table in [PARENT_TABLE, CHILD_TABLE, ORDERS_TABLE] {
        sqlx::query(&format!(
            "CREATE TABLE IF NOT EXISTS {table} (
                id uuid PRIMARY KEY DEFAULT gen_random_uuid() NOT NULL,
                tenant_id uuid NOT NULL,
                code varchar(120),
                status varchar(80),
                data jsonb DEFAULT '{{}}'::jsonb NOT NULL,
                version integer DEFAULT 1 NOT NULL,
                deleted boolean DEFAULT false NOT NULL,
                created_at timestamp with time zone DEFAULT now() NOT NULL,
                updated_at timestamp with time zone DEFAULT now() NOT NULL,
                created_by uuid,
                updated_by uuid
            )"
        ))
        .execute(pool)
        .await
        .unwrap();
    }
}

async fn cleanup(pool: &PgPool, tenant_id: Uuid) {
    for table in [PARENT_TABLE, CHILD_TABLE, ORDERS_TABLE] {
        sqlx::query(&format!(
            "DELETE FROM outbox_events WHERE aggregate_id IN (SELECT id FROM {table} WHERE tenant_id = $1)"
        ))
        .bind(tenant_id)
        .execute(pool)
        .await
        .ok();
    }
    sqlx::query("DELETE FROM workflow_events WHERE tenant_id = $1")
        .bind(tenant_id)
        .execute(pool)
        .await
        .ok();
    for table in [PARENT_TABLE, CHILD_TABLE, ORDERS_TABLE] {
        sqlx::query(&format!("DELETE FROM {table} WHERE tenant_id = $1"))
            .bind(tenant_id)
            .execute(pool)
            .await
            .ok();
    }
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
        computed: None,
    }
}

fn parent_entity() -> EntityDefinition {
    EntityDefinition {
        name: "test.gql_parents".to_string(),
        label: "Parent".to_string(),
        table_name: PARENT_TABLE.to_string(),
        fields: vec![string_field("name"), string_field("secret")],
        list_views: vec![EntityListView {
            name: "default".to_string(),
            label: "Default".to_string(),
            fields: vec!["name".to_string()],
            filters: vec![],
            required_fields: vec![],
            default_sort: None,
            max_limit: 50,
        }],
        workflow: None,
        unique_constraints: vec![],
        audit: None,
    }
}

fn child_entity() -> EntityDefinition {
    EntityDefinition {
        name: "test.gql_children".to_string(),
        label: "Child".to_string(),
        table_name: CHILD_TABLE.to_string(),
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
            computed: None,
        }],
        list_views: vec![EntityListView {
            name: "default".to_string(),
            label: "Default".to_string(),
            fields: vec![],
            filters: vec![],
            required_fields: vec![],
            default_sort: None,
            max_limit: 50,
        }],
        workflow: None,
        unique_constraints: vec![],
        audit: None,
    }
}

fn workflow_entity() -> EntityDefinition {
    EntityDefinition {
        name: "test.gql_orders".to_string(),
        label: "Order".to_string(),
        table_name: ORDERS_TABLE.to_string(),
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
                computed: None,
            },
        ],
        list_views: vec![EntityListView {
            name: "default".to_string(),
            label: "Default".to_string(),
            fields: vec![],
            filters: vec![],
            required_fields: vec![],
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
        unique_constraints: vec![],
        audit: None,
    }
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn full_graphql_lifecycle_reference_expansion_and_field_masking() {
    let pool = connect().await;
    let tenant_id = Uuid::new_v4();
    ensure_tables(&pool).await;

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
        .create("test.gql_parents", &parent_payload, &admin_ctx, None)
        .await
        .unwrap()
    {
        metap_crud::ServiceResult::Ok { data, .. } => data,
        other => panic!("expected parent create to succeed, got {other:?}"),
    };

    for _ in 0..2 {
        let mut child_payload = metap_crud::JsonObject::new();
        child_payload.insert("parentId".to_string(), json!(parent.id.to_string()));
        crud.create("test.gql_children", &child_payload, &admin_ctx, None)
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
        forwarded_bearer_token: None,
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
    ensure_tables(&pool).await;

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

/// Apollo Federation v2 support, added `../metap-docs/docs/roadmap/91-graphql-federation-capability.md`
/// — `build_schema` (no federation) stays byte-for-bit the schema it always was, and
/// `build_schema_with_federation` adds `@key(fields: "id")` + `_entities` without any other
/// caller of this crate having to change anything.
#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn non_federated_schema_has_no_federation_types() {
    let pool = connect().await;
    ensure_tables(&pool).await;

    let mut registry = MetadataRegistry::new();
    registry.register(parent_entity()).unwrap();

    let permissions = Arc::new(PermissionService::new(Box::new(PostgresPolicyStore::new(test_router(
        pool.clone(),
    )))));
    let crud = Arc::new(CrudService::new(
        test_router(pool.clone()),
        Arc::new(ArcSwap::new(Arc::new(registry.clone()))),
        permissions,
    ));

    let schema = build_schema(&registry, crud, SchemaLimits::default()).unwrap();
    let sdl = schema.sdl();
    assert!(
        !sdl.contains("_entities") && !sdl.contains("@key"),
        "build_schema (no federation) must not gain federation types — every existing caller \
         (metap-graphql-http's default router, metap-graphql-gateway) relies on this SDL being \
         unchanged"
    );
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn federated_schema_exposes_key_directive_and_resolves_entities_through_normal_permission_checks() {
    let pool = connect().await;
    let tenant_id = Uuid::new_v4();
    ensure_tables(&pool).await;

    let mut registry = MetadataRegistry::new();
    registry.register(parent_entity()).unwrap();

    let store = PostgresPolicyStore::new(test_router(pool.clone()));
    // Only "admin" can read `test.gql_parents` — "stranger" (no matching policy at all) must be
    // denied by the same deny-by-default entity-level check `Query.testGqlParents(id)` already
    // enforces, proving `_entities` reuses that exact path rather than bypassing permission.
    store
        .create_policy(
            tenant_id,
            "test.gql_parents",
            "read",
            Some(vec!["admin".to_string()]),
            None,
            None,
            None,
            Some(PolicySubject::Context),
            PolicyEffect::Allow,
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
    let mut parent_payload = metap_crud::JsonObject::new();
    parent_payload.insert("name".to_string(), json!("Acme"));
    let parent = match crud
        .create("test.gql_parents", &parent_payload, &admin_ctx, None)
        .await
        .unwrap()
    {
        metap_crud::ServiceResult::Ok { data, .. } => data,
        other => panic!("expected parent create to succeed, got {other:?}"),
    };

    let schema = build_schema_with_federation(&registry, crud.clone(), SchemaLimits::default()).unwrap();
    // Plain `schema.sdl()` (what `GET /graphql/schema.graphql` serves) lists `_entities`/
    // `_service` as real fields, but `@key` only appears in the *federation-flavored* SDL export
    // a real Federation router asks for via the `_service { sdl }` field itself (async-graphql's
    // own `resolve.rs` — that resolver calls `export_sdl(...federation()...)`, `Schema::sdl()`
    // does not) — so this is the query a Federation router would actually run to compose this
    // subgraph, not a Rust-side `.sdl()` call.
    let plain_sdl = schema.sdl();
    assert!(
        plain_sdl.contains("_entities") && plain_sdl.contains("_service"),
        "federated schema's plain SDL must still list _entities/_service as real fields, got:\n{plain_sdl}"
    );
    let service_sdl_query = with_request_data(
        async_graphql::Request::new("{ _service { sdl } }"),
        crud.clone(),
        admin_context(tenant_id),
    );
    let service_sdl_response = schema.execute(service_sdl_query).await;
    assert!(
        service_sdl_response.errors.is_empty(),
        "unexpected errors: {:?}",
        service_sdl_response.errors
    );
    let federation_sdl = service_sdl_response.data.into_json().unwrap()["_service"]["sdl"]
        .as_str()
        .unwrap()
        .to_string();
    assert!(
        federation_sdl.contains(r#"@key(fields: "id")"#),
        "the federation-flavored SDL (`_service.sdl`, what a real Federation router queries) must \
         carry @key(fields: \"id\") on the entity type, got:\n{federation_sdl}"
    );

    // Admin: representation resolves to the real record.
    let query = format!(
        r#"{{ _entities(representations: [{{__typename: "TestGqlParents", id: "{}"}}]) {{
            __typename
            ... on TestGqlParents {{ name }}
        }} }}"#,
        parent.id
    );
    let request = with_request_data(async_graphql::Request::new(&query), crud.clone(), admin_ctx);
    let response = schema.execute(request).await;
    assert!(response.errors.is_empty(), "unexpected errors: {:?}", response.errors);
    let data = response.data.into_json().unwrap();
    assert_eq!(data["_entities"][0]["__typename"], "TestGqlParents");
    assert_eq!(data["_entities"][0]["name"], "Acme");

    // "stranger" (no read policy at all): the deny-by-default entity-level check rejects this
    // exactly like `Query.testGqlParents(id)` would — surfaced as a real GraphQL error (not a
    // silent null), because `async-graphql`'s dynamic-module union resolution has no per-item
    // null path for `_Entity` (verified live above the entity_resolver's own code comment in
    // `schema.rs`); the important guarantee is that the record's data never leaks, not the exact
    // error/null shape.
    let stranger_ctx = RequestContext {
        tenant_id: tenant_id.to_string(),
        user_id: Some(Uuid::new_v4().to_string()),
        roles: Some(vec!["stranger".to_string()]),
        function_id: None,
        context_attributes: None,
        forwarded_bearer_token: None,
    };
    let request = with_request_data(async_graphql::Request::new(&query), crud.clone(), stranger_ctx);
    let response = schema.execute(request).await;
    assert!(
        !response.errors.is_empty(),
        "a caller without read permission must not get the record back"
    );
    assert!(
        response.data.into_json().unwrap()["_entities"].is_null(),
        "a permission error on a non-null field must null out the whole _entities response, not \
         leak partial data"
    );

    cleanup(&pool, tenant_id).await;
}

/// Regression test for a real bug found live 2026-09-26
/// (`../metap-docs/docs/roadmap/95-platform-graphql-fields.md`): audit 04 B#2 moved every
/// resolver onto `service_result_to_gql`'s `extensions.code`/`status`/`fieldErrors` error shape
/// *except* `get`/`{entity}List`, which were missed and kept a bare `"{status}: {message}"`
/// string with no `extensions` at all — silently breaking any client-side error handler that
/// branches on `extensions` (every other field's errors already required it). Asserts both
/// fields now carry the same shape a mutation's permission error does.
#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn get_and_list_permission_errors_carry_the_same_extensions_shape_mutations_do() {
    let pool = connect().await;
    let tenant_id = Uuid::new_v4();
    ensure_tables(&pool).await;

    let mut registry = MetadataRegistry::new();
    registry.register(parent_entity()).unwrap();
    let registry = Arc::new(registry);

    let permissions = Arc::new(PermissionService::new(Box::new(PostgresPolicyStore::new(test_router(
        pool.clone(),
    )))));
    let crud = Arc::new(CrudService::new(
        test_router(pool.clone()),
        Arc::new(ArcSwap::new(registry.clone())),
        permissions,
    ));

    let admin_ctx = admin_context(tenant_id);
    let mut payload = metap_crud::JsonObject::new();
    payload.insert("name".to_string(), json!("Acme"));
    payload.insert("secret".to_string(), json!("classified"));
    let parent = match crud
        .create("test.gql_parents", &payload, &admin_ctx, None)
        .await
        .unwrap()
    {
        metap_crud::ServiceResult::Ok { data, .. } => data,
        other => panic!("expected create to succeed, got {other:?}"),
    };

    let schema = build_schema(&registry, crud.clone(), SchemaLimits::default()).unwrap();
    // No policy at all was ever created for this tenant/role — deny-by-default rejects both
    // calls below, exactly the permission error shape a real caller would hit.
    let stranger_ctx = RequestContext {
        tenant_id: tenant_id.to_string(),
        user_id: Some(Uuid::new_v4().to_string()),
        roles: Some(vec!["stranger".to_string()]),
        function_id: None,
        context_attributes: None,
        forwarded_bearer_token: None,
    };

    let get_query = format!(r#"{{ testGqlParents(id: "{}") {{ id }} }}"#, parent.id);
    let request = with_request_data(
        async_graphql::Request::new(&get_query),
        crud.clone(),
        stranger_ctx.clone(),
    );
    let response = schema.execute(request).await;
    assert!(!response.errors.is_empty(), "expected a permission error from get");
    let extensions = response.errors[0]
        .extensions
        .as_ref()
        .expect("get's permission error must carry extensions, same as a mutation's");
    assert!(extensions.get("code").is_some(), "missing extensions.code");
    assert!(extensions.get("status").is_some(), "missing extensions.status");

    let list_query = "{ testGqlParentsList { records { id } } }";
    let request = with_request_data(async_graphql::Request::new(list_query), crud.clone(), stranger_ctx);
    let response = schema.execute(request).await;
    assert!(!response.errors.is_empty(), "expected a permission error from list");
    let extensions = response.errors[0]
        .extensions
        .as_ref()
        .expect("list's permission error must carry extensions, same as a mutation's");
    assert!(extensions.get("code").is_some(), "missing extensions.code");
    assert!(extensions.get("status").is_some(), "missing extensions.status");

    cleanup(&pool, tenant_id).await;
}
