//! E2E tests running the full `CrudService` lifecycle against the repo's real dev Postgres.
//! `#[ignore]`d — see `metap-query/tests/query_planner_postgres.rs`'s doc comment for the
//! convention (unit tests never touch a DB; these run explicitly via
//! `cargo test -- --ignored`). This is the integration point for every crate built in
//! Migration Order steps 3–6 — the most important place for real, not just unit-tested,
//! confidence.

mod support;

use metap_control::PostgresPolicyStore;
use metap_crud::{CrudService, JsonObject, ServiceResult};
use metap_metadata::{EntityDefinition, EntityField, EntityWorkflow, FieldKind, MetadataRegistry, WorkflowTransition};
use metap_permission::{
    ConditionOp, PermissionService, PolicyCondition, PolicyEffect, PolicyStore, PolicySubject, PolicyValue,
    RequestContext,
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
                storage: None,
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
                storage: None,
            },
            EntityField {
                name: "status".to_string(),
                label: "Status".to_string(),
                kind: FieldKind::Enum,
                required: None,
                indexed: None,
                unique: None,
                enum_values: Some(vec!["draft".to_string(), "approved".to_string(), "closed".to_string()]),
                ref_entity: None,
                ref_display_field: None,
                searchable: None,
                search_mode: None,
                sortable: None,
                storage: None,
            },
            EntityField {
                name: "resolution".to_string(),
                label: "Resolution".to_string(),
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
            },
            EntityField {
                name: "closedBy".to_string(),
                label: "Closed By".to_string(),
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
            transitions: vec![
                WorkflowTransition {
                    action: "approve".to_string(),
                    from: "draft".to_string(),
                    to: "approved".to_string(),
                    label: "Approve".to_string(),
                    guard: Some(PolicyCondition::Attribute {
                        attribute: "amount".to_string(),
                        op: ConditionOp::Eq,
                        value: PolicyValue::Literal { literal: json!(100) },
                    }),
                    validator: None,
                    set_fields: None,
                },
                WorkflowTransition {
                    action: "close".to_string(),
                    from: "approved".to_string(),
                    to: "closed".to_string(),
                    label: "Close".to_string(),
                    guard: None,
                    // Requires the caller's transition payload to actually set `resolution` —
                    // something `run_guard` alone can't express since it only sees pre-transition
                    // data. Exercises `CrudService::transition`'s payload-merge + validator wiring.
                    validator: Some(PolicyCondition::Attribute {
                        attribute: "resolution".to_string(),
                        op: ConditionOp::Neq,
                        value: PolicyValue::Literal { literal: json!(null) },
                    }),
                    // System-computed: who closed it, taken from the caller's own context, not
                    // the payload. Exercises `apply_set_fields`.
                    set_fields: Some(std::collections::HashMap::from([(
                        "closedBy".to_string(),
                        PolicyValue::FromContext {
                            from_context: "userId".to_string(),
                        },
                    )])),
                },
            ],
        }),
    }
}

/// A second entity, distinct from `test_entity()`, with a `sku` field declared
/// `unique: true`. Mirrors `EntityField.unique`'s enforcement: purely a Postgres unique
/// index (`crates/metap-peripherals/src/index_reconciler.rs`), reconciled at boot/hot-reload
/// in production but never by `CrudService` itself — so this test creates the same index by
/// hand rather than pulling in `metap-peripherals` as a dev-dependency just for this.
fn unique_field_entity() -> EntityDefinition {
    EntityDefinition {
        name: "test.unique_orders".to_string(),
        label: "Unique Order".to_string(),
        table_name: "records".to_string(),
        fields: vec![EntityField {
            name: "sku".to_string(),
            label: "SKU".to_string(),
            kind: FieldKind::String,
            required: Some(true),
            indexed: None,
            unique: Some(true),
            enum_values: None,
            ref_entity: None,
            ref_display_field: None,
            searchable: None,
            search_mode: None,
            sortable: None,
            storage: None,
        }],
        list_views: vec![],
        workflow: None,
    }
}

/// A referenced-by-another-entity pair for the reference-integrity guard tests
/// (`docs/architectures/11-risks.md`): `test.children.parentId` is a `Reference` field pointing
/// at `test.parents`.
fn parent_entity() -> EntityDefinition {
    EntityDefinition {
        name: "test.parents".to_string(),
        label: "Parent".to_string(),
        table_name: "records".to_string(),
        fields: vec![EntityField {
            name: "name".to_string(),
            label: "Name".to_string(),
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
        }],
        list_views: vec![],
        workflow: None,
    }
}

fn child_entity() -> EntityDefinition {
    EntityDefinition {
        name: "test.children".to_string(),
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
            ref_entity: Some("test.parents".to_string()),
            // Exercises `CrudService::hydrate_related_display` ("Mode 2",
            // `docs/roadmap.md`) — `test.parents` has a `name` field (see `parent_entity()`).
            ref_display_field: Some("name".to_string()),
            searchable: None,
            search_mode: None,
            sortable: None,
            storage: None,
        }],
        list_views: vec![],
        workflow: None,
    }
}

/// A *second*, distinct entity referencing `test.parents` — for testing that
/// `find_referencing_record`'s combined single-query check (`docs/roadmap.md`, code review
/// 2026-08-22) catches a reference through *either* `test.children` or this entity, not just
/// whichever one happens to be first in `referencing_fields`'s result.
fn grandchild_entity() -> EntityDefinition {
    EntityDefinition {
        name: "test.grandchildren".to_string(),
        label: "Grandchild".to_string(),
        table_name: "records".to_string(),
        fields: vec![EntityField {
            name: "grandparentId".to_string(),
            label: "Grandparent".to_string(),
            kind: FieldKind::Reference,
            required: None,
            indexed: None,
            unique: None,
            enum_values: None,
            ref_entity: Some("test.parents".to_string()),
            ref_display_field: None,
            searchable: None,
            search_mode: None,
            sortable: None,
            storage: None,
        }],
        list_views: vec![],
        workflow: None,
    }
}

/// Self-referencing entity (like `crm.customers.referredBy`) for the reference-integrity
/// guard's self-reference regression test — a record whose own `Reference` field points at
/// itself must not be blocked from deleting itself.
fn self_ref_entity() -> EntityDefinition {
    EntityDefinition {
        name: "test.nodes".to_string(),
        label: "Node".to_string(),
        table_name: "records".to_string(),
        fields: vec![EntityField {
            name: "parentNodeId".to_string(),
            label: "Parent Node".to_string(),
            kind: FieldKind::Reference,
            required: None,
            indexed: None,
            unique: None,
            enum_values: None,
            ref_entity: Some("test.nodes".to_string()),
            ref_display_field: None,
            searchable: None,
            search_mode: None,
            sortable: None,
            storage: None,
        }],
        list_views: vec![],
        workflow: None,
    }
}

async fn ensure_sku_unique_index(pool: &PgPool) {
    sqlx::query(
        "CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS uniq_records_test_unique_orders_sku \
         ON records (tenant_id, (jsonb_extract_path_text(data, 'sku'))) \
         WHERE entity = 'test.unique_orders' AND deleted = false",
    )
    .execute(pool)
    .await
    .unwrap();
}

async fn connect() -> PgPool {
    let database_url = std::env::var("DATABASE_URL").expect("DATABASE_URL required for this e2e test");
    PgPoolOptions::new()
        .max_connections(5)
        .connect(&database_url)
        .await
        .unwrap()
}

/// No `control.tenants` row is ever inserted by these tests, so `Router::begin` always takes
/// the unregistered-tenant fallback (public schema) — same behavior `CrudService` had before
/// the Router refactor, which is exactly what these tests exercise.
fn test_router(pool: PgPool) -> metap_control::Router {
    let registry = std::sync::Arc::new(metap_control::PostgresTenantRegistry::new(pool.clone()));
    metap_control::Router::new(
        pool,
        metap_control::RegistryCache::new(registry),
        std::sync::Arc::new(metap_control::EnvStore),
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

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn full_lifecycle_create_get_update_transition_delete() {
    let pool = connect().await;
    let tenant_id = Uuid::new_v4();
    let ctx = admin_context(tenant_id);

    let mut registry = MetadataRegistry::new();
    registry.register(test_entity()).unwrap();
    let permissions = PermissionService::new(Box::new(PostgresPolicyStore::new(test_router(pool.clone()))));
    let crud = CrudService::new(
        test_router(pool.clone()),
        std::sync::Arc::new(arc_swap::ArcSwap::new(std::sync::Arc::new(registry))),
        std::sync::Arc::new(permissions),
    );

    // create
    let mut payload = JsonObject::new();
    payload.insert("name".to_string(), json!("First order"));
    payload.insert("amount".to_string(), json!(50));
    let created = match crud.create("test.orders", &payload, &ctx).await.unwrap() {
        ServiceResult::Ok { data, .. } => data,
        other => panic!("expected create to succeed, got {other:?}"),
    };
    assert_eq!(
        created.status.as_deref(),
        Some("draft"),
        "getInitialStatus must set draft"
    );
    assert_eq!(created.version, 1);

    // create validation failure: missing required "name"
    let mut bad_payload = JsonObject::new();
    bad_payload.insert("amount".to_string(), json!(1));
    match crud.create("test.orders", &bad_payload, &ctx).await.unwrap() {
        ServiceResult::Err {
            status,
            error,
            field_errors,
            ..
        } => {
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
    assert!(
        !capabilities.transitions[0].available,
        "guard requires amount == 100, current is 50"
    );

    // update with stale version -> 409
    let mut update_payload = JsonObject::new();
    update_payload.insert("amount".to_string(), json!(100));
    match crud
        .update("test.orders", created.id, 999, &update_payload, &ctx)
        .await
        .unwrap()
    {
        ServiceResult::Err { status, error, .. } => {
            assert_eq!(status, 409);
            assert_eq!(error, "version_conflict");
        }
        other => panic!("expected version_conflict, got {other:?}"),
    }

    // update with correct version -> succeeds, version increments
    let updated = match crud
        .update("test.orders", created.id, created.version, &update_payload, &ctx)
        .await
        .unwrap()
    {
        ServiceResult::Ok { data, .. } => data,
        other => panic!("expected update to succeed, got {other:?}"),
    };
    assert_eq!(updated.version, 2);
    assert_eq!(updated.data["amount"], json!(100));
    assert_eq!(
        updated.data["status"],
        json!("draft"),
        "status field must not change via update, only via transition"
    );

    // transition guard now passes (amount == 100)
    let transitioned = match crud
        .transition("test.orders", created.id, "approve", updated.version, None, &ctx)
        .await
        .unwrap()
    {
        ServiceResult::Ok { data, .. } => data,
        other => panic!("expected transition to succeed, got {other:?}"),
    };
    assert_eq!(transitioned.status.as_deref(), Some("approved"));
    assert_eq!(transitioned.data["status"], json!("approved"));
    assert_eq!(transitioned.version, 3);

    // transition again from a now-invalid from-state -> invalid_transition
    match crud
        .transition("test.orders", created.id, "approve", transitioned.version, None, &ctx)
        .await
        .unwrap()
    {
        ServiceResult::Err { status, error, .. } => {
            assert_eq!(status, 409);
            assert_eq!(error, "invalid_transition");
        }
        other => panic!("expected invalid_transition, got {other:?}"),
    }

    // delete (soft)
    let deleted = match crud
        .delete("test.orders", created.id, transitioned.version, &ctx)
        .await
        .unwrap()
    {
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
    let event_count: i64 =
        sqlx::query_scalar("SELECT count(*) FROM workflow_events WHERE tenant_id = $1 AND record_id = $2")
            .bind(tenant_id)
            .bind(created.id)
            .fetch_one(&pool)
            .await
            .unwrap();
    assert_eq!(event_count, 1);

    // outbox got: record.created, record.updated, workflow.transitioned, record.deleted
    let topics: Vec<String> =
        sqlx::query_scalar("SELECT topic FROM outbox_events WHERE aggregate_id = $1 ORDER BY created_at")
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

/// Exercises the 3-part transition upgrade end to end: a caller-submitted payload merged
/// into the transition write, a `validator` that runs against that merged data (rejecting a
/// payload that omits a required field, something `guard` alone can't do since it only ever
/// sees pre-transition data), and `set_fields` layering in a system-computed value from the
/// caller's own context. See `test_entity()`'s `"close"` transition.
#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn transition_payload_is_validated_and_set_fields_are_applied() {
    let pool = connect().await;
    let tenant_id = Uuid::new_v4();
    let ctx = admin_context(tenant_id);
    let caller_user_id = ctx.user_id.clone().unwrap();

    let mut registry = MetadataRegistry::new();
    registry.register(test_entity()).unwrap();
    let permissions = PermissionService::new(Box::new(PostgresPolicyStore::new(test_router(pool.clone()))));
    let crud = CrudService::new(
        test_router(pool.clone()),
        std::sync::Arc::new(arc_swap::ArcSwap::new(std::sync::Arc::new(registry))),
        std::sync::Arc::new(permissions),
    );

    let mut payload = JsonObject::new();
    payload.insert("name".to_string(), json!("Order to close"));
    payload.insert("amount".to_string(), json!(100));
    let created = match crud.create("test.orders", &payload, &ctx).await.unwrap() {
        ServiceResult::Ok { data, .. } => data,
        other => panic!("expected create to succeed, got {other:?}"),
    };
    let approved = match crud
        .transition("test.orders", created.id, "approve", created.version, None, &ctx)
        .await
        .unwrap()
    {
        ServiceResult::Ok { data, .. } => data,
        other => panic!("expected approve to succeed, got {other:?}"),
    };

    // close without a `resolution` in the payload -> validator rejects it
    match crud
        .transition("test.orders", created.id, "close", approved.version, None, &ctx)
        .await
        .unwrap()
    {
        ServiceResult::Err { status, error, .. } => {
            assert_eq!(status, 422);
            assert_eq!(error, "validator_failed");
        }
        other => panic!("expected validator_failed, got {other:?}"),
    }

    // close with `resolution` set -> validator passes, set_fields stamps `closedBy` from context
    let mut close_payload = JsonObject::new();
    close_payload.insert("resolution".to_string(), json!("fixed"));
    let closed = match crud
        .transition(
            "test.orders",
            created.id,
            "close",
            approved.version,
            Some(&close_payload),
            &ctx,
        )
        .await
        .unwrap()
    {
        ServiceResult::Ok { data, .. } => data,
        other => panic!("expected close to succeed, got {other:?}"),
    };
    assert_eq!(closed.status.as_deref(), Some("closed"));
    assert_eq!(closed.data["resolution"], json!("fixed"));
    assert_eq!(closed.data["closedBy"], json!(caller_user_id));

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
    let permissions = PermissionService::new(Box::new(PostgresPolicyStore::new(test_router(pool.clone()))));
    let crud = CrudService::new(
        test_router(pool.clone()),
        std::sync::Arc::new(arc_swap::ArcSwap::new(std::sync::Arc::new(registry))),
        std::sync::Arc::new(permissions),
    );

    for name in ["a", "b", "c"] {
        let mut payload = JsonObject::new();
        payload.insert("name".to_string(), json!(name));
        crud.create("test.orders", &payload, &ctx).await.unwrap();
    }

    let input = ListInput {
        limit: 50,
        ..Default::default()
    };
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
    let store = PostgresPolicyStore::new(test_router(pool.clone()));

    // Entity-level "create" policy open to "support" too — otherwise `check_action`'s
    // default-deny-when-unconfigured (`docs/roadmap.md`'s permission-review findings,
    // 2026-08-21) would reject this request before the field-level check below ever runs, and
    // this test would observe a generic entity-level 403 instead of the field-specific one it's
    // actually testing.
    store
        .create_policy(
            tenant_id,
            "test.orders",
            "create",
            Some(vec!["support".to_string(), "sales".to_string()]),
            None,
            None,
            None,
            Some(PolicySubject::Context),
            PolicyEffect::Allow,
        )
        .await
        .unwrap();

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
            PolicyEffect::Allow,
        )
        .await
        .unwrap();

    let permissions = PermissionService::new(Box::new(store));
    let crud = CrudService::new(
        test_router(pool.clone()),
        std::sync::Arc::new(arc_swap::ArcSwap::new(std::sync::Arc::new(registry))),
        std::sync::Arc::new(permissions),
    );

    let ctx = RequestContext {
        tenant_id: tenant_id.to_string(),
        user_id: Some(Uuid::new_v4().to_string()),
        roles: Some(vec!["support".to_string()]), // not "sales" — must be denied on "amount"
        function_id: None,
        context_attributes: None,
    };

    let mut payload = JsonObject::new();
    payload.insert("name".to_string(), json!("blocked"));
    payload.insert("amount".to_string(), json!(1));

    match crud.create("test.orders", &payload, &ctx).await.unwrap() {
        ServiceResult::Err {
            status, field_errors, ..
        } => {
            assert_eq!(status, 403);
            assert!(field_errors.unwrap().contains_key("amount"));
        }
        other => panic!("expected a field-level 403 on amount, got {other:?}"),
    }

    cleanup(&pool, tenant_id).await;
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn unique_field_violation_is_a_clean_409_not_a_500() {
    let pool = connect().await;
    ensure_sku_unique_index(&pool).await;
    let tenant_id = Uuid::new_v4();
    let ctx = admin_context(tenant_id);

    let mut registry = MetadataRegistry::new();
    registry.register(unique_field_entity()).unwrap();
    let permissions = PermissionService::new(Box::new(PostgresPolicyStore::new(test_router(pool.clone()))));
    let crud = CrudService::new(
        test_router(pool.clone()),
        std::sync::Arc::new(arc_swap::ArcSwap::new(std::sync::Arc::new(registry))),
        std::sync::Arc::new(permissions),
    );

    let mut payload = JsonObject::new();
    payload.insert("sku".to_string(), json!("ABC-1"));
    let first = match crud.create("test.unique_orders", &payload, &ctx).await.unwrap() {
        ServiceResult::Ok { data, .. } => data,
        other => panic!("expected first create to succeed, got {other:?}"),
    };

    // second create with the same sku -> 409 unique_violation, field_errors names "sku",
    // not an unhandled 500 from the raw DB error propagating through `?`.
    match crud.create("test.unique_orders", &payload, &ctx).await.unwrap() {
        ServiceResult::Err {
            status,
            error,
            field_errors,
            ..
        } => {
            assert_eq!(status, 409);
            assert_eq!(error, "unique_violation");
            assert!(field_errors.unwrap().contains_key("sku"));
        }
        other => panic!("expected unique_violation on duplicate create, got {other:?}"),
    }

    // a second, distinct record, then updated to collide with the first -> same 409 on update.
    let mut other_payload = JsonObject::new();
    other_payload.insert("sku".to_string(), json!("ABC-2"));
    let second = match crud.create("test.unique_orders", &other_payload, &ctx).await.unwrap() {
        ServiceResult::Ok { data, .. } => data,
        other => panic!("expected second create to succeed, got {other:?}"),
    };
    match crud
        .update("test.unique_orders", second.id, second.version, &payload, &ctx)
        .await
        .unwrap()
    {
        ServiceResult::Err {
            status,
            error,
            field_errors,
            ..
        } => {
            assert_eq!(status, 409);
            assert_eq!(error, "unique_violation");
            assert!(field_errors.unwrap().contains_key("sku"));
        }
        other => panic!("expected unique_violation on colliding update, got {other:?}"),
    }

    // update did not bump the record's version (the write never actually happened)
    let (refetched, _) = match crud.get("test.unique_orders", second.id, &ctx).await.unwrap() {
        ServiceResult::Ok { data, .. } => data,
        other => panic!("expected get to succeed, got {other:?}"),
    };
    assert_eq!(refetched.version, second.version);

    let _ = first;
    cleanup(&pool, tenant_id).await;
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn delete_is_rejected_when_another_record_still_references_it() {
    let pool = connect().await;
    let tenant_id = Uuid::new_v4();
    let ctx = admin_context(tenant_id);

    let mut registry = MetadataRegistry::new();
    registry.register(parent_entity()).unwrap();
    registry.register(child_entity()).unwrap();
    let permissions = PermissionService::new(Box::new(PostgresPolicyStore::new(test_router(pool.clone()))));
    let crud = CrudService::new(
        test_router(pool.clone()),
        std::sync::Arc::new(arc_swap::ArcSwap::new(std::sync::Arc::new(registry))),
        std::sync::Arc::new(permissions),
    );

    let mut parent_payload = JsonObject::new();
    parent_payload.insert("name".to_string(), json!("Parent A"));
    let parent = match crud.create("test.parents", &parent_payload, &ctx).await.unwrap() {
        ServiceResult::Ok { data, .. } => data,
        other => panic!("expected create to succeed, got {other:?}"),
    };

    let mut child_payload = JsonObject::new();
    child_payload.insert("parentId".to_string(), json!(parent.id));
    crud.create("test.children", &child_payload, &ctx).await.unwrap();

    match crud
        .delete("test.parents", parent.id, parent.version, &ctx)
        .await
        .unwrap()
    {
        ServiceResult::Err { status, error, .. } => {
            assert_eq!(status, 409);
            assert_eq!(error, "record_referenced");
        }
        other => panic!("expected record_referenced, got {other:?}"),
    }

    // the parent must still exist — the rejected delete must not have partially applied
    match crud.get("test.parents", parent.id, &ctx).await.unwrap() {
        ServiceResult::Ok { .. } => {}
        other => panic!("expected parent to still exist after rejected delete, got {other:?}"),
    }

    cleanup(&pool, tenant_id).await;
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn delete_succeeds_once_the_referencing_record_is_gone() {
    let pool = connect().await;
    let tenant_id = Uuid::new_v4();
    let ctx = admin_context(tenant_id);

    let mut registry = MetadataRegistry::new();
    registry.register(parent_entity()).unwrap();
    registry.register(child_entity()).unwrap();
    let permissions = PermissionService::new(Box::new(PostgresPolicyStore::new(test_router(pool.clone()))));
    let crud = CrudService::new(
        test_router(pool.clone()),
        std::sync::Arc::new(arc_swap::ArcSwap::new(std::sync::Arc::new(registry))),
        std::sync::Arc::new(permissions),
    );

    let mut parent_payload = JsonObject::new();
    parent_payload.insert("name".to_string(), json!("Parent B"));
    let parent = match crud.create("test.parents", &parent_payload, &ctx).await.unwrap() {
        ServiceResult::Ok { data, .. } => data,
        other => panic!("expected create to succeed, got {other:?}"),
    };

    let mut child_payload = JsonObject::new();
    child_payload.insert("parentId".to_string(), json!(parent.id));
    let child = match crud.create("test.children", &child_payload, &ctx).await.unwrap() {
        ServiceResult::Ok { data, .. } => data,
        other => panic!("expected create to succeed, got {other:?}"),
    };

    // delete the referencing child first
    crud.delete("test.children", child.id, child.version, &ctx)
        .await
        .unwrap();

    // parent delete now succeeds — no live reference left
    match crud
        .delete("test.parents", parent.id, parent.version, &ctx)
        .await
        .unwrap()
    {
        ServiceResult::Ok { .. } => {}
        other => panic!("expected delete to succeed once the referencing child is gone, got {other:?}"),
    }

    cleanup(&pool, tenant_id).await;
}

/// "Mode 2" batch display hydration (`docs/roadmap.md`) — `CrudService::list`'s
/// `hydrate_related_display`. Two parents, three children (two pointing at the same parent, one
/// at the other, one with no parent at all) — confirms hydration resolves the right value per
/// row (not just "some value"), batches correctly when multiple rows share a related id, and
/// leaves a row with no reference value alone rather than erroring.
#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn list_hydrates_related_display_for_reference_fields_with_display_field() {
    let pool = connect().await;
    let tenant_id = Uuid::new_v4();
    let ctx = admin_context(tenant_id);

    let mut registry = MetadataRegistry::new();
    registry.register(parent_entity()).unwrap();
    registry.register(child_entity()).unwrap();
    let permissions = PermissionService::new(Box::new(PostgresPolicyStore::new(test_router(pool.clone()))));
    let crud = CrudService::new(
        test_router(pool.clone()),
        std::sync::Arc::new(arc_swap::ArcSwap::new(std::sync::Arc::new(registry))),
        std::sync::Arc::new(permissions),
    );

    let mut a_payload = JsonObject::new();
    a_payload.insert("name".to_string(), json!("Parent A"));
    let parent_a = match crud.create("test.parents", &a_payload, &ctx).await.unwrap() {
        ServiceResult::Ok { data, .. } => data,
        other => panic!("expected create to succeed, got {other:?}"),
    };
    let mut b_payload = JsonObject::new();
    b_payload.insert("name".to_string(), json!("Parent B"));
    let parent_b = match crud.create("test.parents", &b_payload, &ctx).await.unwrap() {
        ServiceResult::Ok { data, .. } => data,
        other => panic!("expected create to succeed, got {other:?}"),
    };

    let mut child_of_a1 = JsonObject::new();
    child_of_a1.insert("parentId".to_string(), json!(parent_a.id));
    crud.create("test.children", &child_of_a1, &ctx).await.unwrap();
    let mut child_of_a2 = JsonObject::new();
    child_of_a2.insert("parentId".to_string(), json!(parent_a.id));
    crud.create("test.children", &child_of_a2, &ctx).await.unwrap();
    let mut child_of_b = JsonObject::new();
    child_of_b.insert("parentId".to_string(), json!(parent_b.id));
    crud.create("test.children", &child_of_b, &ctx).await.unwrap();
    // no parentId at all — the field isn't required
    crud.create("test.children", &JsonObject::new(), &ctx).await.unwrap();

    let input = ListInput {
        limit: 50,
        ..Default::default()
    };
    let page = match crud.list("test.children", &input, &ctx).await.unwrap() {
        ServiceResult::Ok { data, .. } => data,
        other => panic!("expected list to succeed, got {other:?}"),
    };
    assert_eq!(page.len(), 4);

    for record in &page {
        let parent_id = record.data.get("parentId").and_then(|v| v.as_str());
        match parent_id {
            Some(id) if id == parent_a.id.to_string() => {
                assert_eq!(
                    record.related_display.as_ref().and_then(|d| d.get("parentId")),
                    Some(&"Parent A".to_string()),
                    "record {record:?} should have resolved parentId to Parent A's name"
                );
            }
            Some(id) if id == parent_b.id.to_string() => {
                assert_eq!(
                    record.related_display.as_ref().and_then(|d| d.get("parentId")),
                    Some(&"Parent B".to_string()),
                    "record {record:?} should have resolved parentId to Parent B's name"
                );
            }
            None => {
                assert!(
                    record.related_display.is_none(),
                    "a record with no parentId at all must not get a related_display entry"
                );
            }
            Some(other) => panic!("unexpected parentId {other}"),
        }
    }

    cleanup(&pool, tenant_id).await;
}

/// Regression test for a real bug found in code review (2026-08-22): the reference-integrity
/// guard's `referencing_fields` deliberately includes self-referencing fields (like
/// `crm.customers.referredBy`), but the guard's SELECT didn't exclude the record's own row —
/// so a record whose self-reference pointed at itself matched itself and could never be
/// deleted.
#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn delete_succeeds_for_a_record_whose_self_reference_points_at_itself() {
    let pool = connect().await;
    let tenant_id = Uuid::new_v4();
    let ctx = admin_context(tenant_id);

    let mut registry = MetadataRegistry::new();
    registry.register(self_ref_entity()).unwrap();
    let permissions = PermissionService::new(Box::new(PostgresPolicyStore::new(test_router(pool.clone()))));
    let crud = CrudService::new(
        test_router(pool.clone()),
        std::sync::Arc::new(arc_swap::ArcSwap::new(std::sync::Arc::new(registry))),
        std::sync::Arc::new(permissions),
    );

    let node = match crud.create("test.nodes", &JsonObject::new(), &ctx).await.unwrap() {
        ServiceResult::Ok { data, .. } => data,
        other => panic!("expected create to succeed, got {other:?}"),
    };

    let mut self_ref_payload = JsonObject::new();
    self_ref_payload.insert("parentNodeId".to_string(), json!(node.id));
    let node = match crud
        .update("test.nodes", node.id, node.version, &self_ref_payload, &ctx)
        .await
        .unwrap()
    {
        ServiceResult::Ok { data, .. } => data,
        other => panic!("expected update to succeed, got {other:?}"),
    };

    match crud.delete("test.nodes", node.id, node.version, &ctx).await.unwrap() {
        ServiceResult::Ok { .. } => {}
        other => panic!("expected delete to succeed for a record whose self-reference points at itself, got {other:?}"),
    }

    cleanup(&pool, tenant_id).await;
}

/// Regression test for the reference-integrity guard's combined single-query check (found
/// missing test coverage in code review, 2026-08-22, alongside the N-sequential-queries fix
/// itself): a parent referenced by *two different* entities must still be blocked from
/// deletion no matter which one holds the live reference.
#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn delete_is_rejected_when_referenced_by_any_of_multiple_referencing_entities() {
    let pool = connect().await;
    let tenant_id = Uuid::new_v4();
    let ctx = admin_context(tenant_id);

    let mut registry = MetadataRegistry::new();
    registry.register(parent_entity()).unwrap();
    registry.register(child_entity()).unwrap();
    registry.register(grandchild_entity()).unwrap();
    let permissions = PermissionService::new(Box::new(PostgresPolicyStore::new(test_router(pool.clone()))));
    let crud = CrudService::new(
        test_router(pool.clone()),
        std::sync::Arc::new(arc_swap::ArcSwap::new(std::sync::Arc::new(registry))),
        std::sync::Arc::new(permissions),
    );

    let mut parent_payload = JsonObject::new();
    parent_payload.insert("name".to_string(), json!("Parent A"));
    let parent = match crud.create("test.parents", &parent_payload, &ctx).await.unwrap() {
        ServiceResult::Ok { data, .. } => data,
        other => panic!("expected create to succeed, got {other:?}"),
    };

    // only the grandchild references the parent — no child does.
    let mut grandchild_payload = JsonObject::new();
    grandchild_payload.insert("grandparentId".to_string(), json!(parent.id));
    crud.create("test.grandchildren", &grandchild_payload, &ctx)
        .await
        .unwrap();

    match crud
        .delete("test.parents", parent.id, parent.version, &ctx)
        .await
        .unwrap()
    {
        ServiceResult::Err {
            status, error, message, ..
        } => {
            assert_eq!(status, 409);
            assert_eq!(error, "record_referenced");
            assert!(
                message.is_some_and(|m| m.contains("test.grandchildren")),
                "error message should name the actual referencing entity"
            );
        }
        other => panic!("expected record_referenced, got {other:?}"),
    }

    cleanup(&pool, tenant_id).await;
}

/// Sustained, concurrent load test of the *real* complex-workflow path — not a single flat
/// table with simple filters (that benchmark, `docs/roadmap.md` 2026-08-22/23, was correctly
/// called out as unrepresentative). Runs `CrudService::list()` directly (no HTTP layer, no
/// per-IP rate limiter — this measures business-logic capacity, not an unrelated HTTP-layer
/// throttle) against `hr.departments` -> `hr.employees` -> `helpdesk.tickets` (2 `Reference`
/// fields, a workflow, department-scoped record-level ABAC via `fromContext`), seeded with
/// 500K real, related tickets across 200 real employees in 20 real departments (seeded
/// separately, out-of-band, before this runs — see the benchmarking session). Every `list()`
/// call here pays: a context-level permission check, a record-policy snapshot load, the base
/// filtered `SELECT`, and `hydrate_related_display` for *both* `Reference` fields (each with
/// its own permission check + batch fetch + per-row record-condition check) — the actual cost
/// shape a real org-scoped list view has, not an approximation.
///
/// `#[ignore]`d like every other e2e test, but not meant to run in a normal `--ignored` pass —
/// it needs the specific out-of-band seed above and runs for `DURATION_SECS`. Prints results to
/// stderr (`--nocapture`) rather than asserting thresholds, same "read the numbers yourself"
/// spirit as `apps/crm-server/scripts/bench-queries.sh`.
#[tokio::test]
#[ignore = "manual benchmark: requires the out-of-band hr/helpdesk seed, not part of a normal e2e run"]
async fn sustained_concurrent_list_against_a_real_multi_entity_abac_workflow() {
    use std::sync::Arc;
    use std::time::Duration;

    use support::{run_sustained_load, LoadTestConfig};

    const DURATION_SECS: u64 = 600;
    const CONCURRENCY: usize = 20;
    // Fixed dev tenant (`pnpm seed:admin`'s default) — where the out-of-band seed script put
    // 500K helpdesk.tickets across 200 hr.employees in 20 hr.departments.
    let tenant_id: Uuid = "00000000-0000-0000-0000-000000000001".parse().unwrap();

    // Not `connect()` — its pool is intentionally small (5) for the rest of this file's
    // lightweight, mostly-sequential tests. `list()` itself needs one connection per
    // in-flight call (permission checks + base query + hydration all share one transaction),
    // so `CONCURRENCY` concurrent workers need at least that many connections available or
    // most of them just time out waiting for the pool, not for anything CrudService did.
    let database_url = std::env::var("DATABASE_URL").expect("DATABASE_URL required for this e2e test");
    let pool = PgPoolOptions::new()
        .max_connections(CONCURRENCY as u32 + 5)
        .connect(&database_url)
        .await
        .unwrap();
    let dept_ids: Vec<Uuid> =
        sqlx::query_scalar("SELECT id FROM records WHERE entity = 'hr.departments' AND tenant_id = $1 ORDER BY id")
            .bind(tenant_id)
            .fetch_all(&pool)
            .await
            .expect("hr.departments must already be seeded (out-of-band) before this test runs");
    assert!(
        !dept_ids.is_empty(),
        "no hr.departments found for the fixed dev tenant — seed first"
    );

    fn ref_field(name: &str, ref_entity: &str, ref_display_field: &str, indexed: bool) -> EntityField {
        EntityField {
            name: name.to_string(),
            label: name.to_string(),
            kind: FieldKind::Reference,
            required: None,
            indexed: indexed.then_some(true),
            unique: None,
            enum_values: None,
            ref_entity: Some(ref_entity.to_string()),
            ref_display_field: Some(ref_display_field.to_string()),
            searchable: None,
            search_mode: None,
            sortable: None,
            storage: None,
        }
    }
    fn plain_field(name: &str, kind: FieldKind) -> EntityField {
        EntityField {
            name: name.to_string(),
            label: name.to_string(),
            kind,
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
        }
    }

    let mut registry = MetadataRegistry::new();
    registry
        .register(EntityDefinition {
            name: "hr.departments".to_string(),
            label: "Department".to_string(),
            table_name: "records".to_string(),
            fields: vec![plain_field("name", FieldKind::String)],
            list_views: vec![],
            workflow: None,
        })
        .unwrap();
    registry
        .register(EntityDefinition {
            name: "hr.employees".to_string(),
            label: "Employee".to_string(),
            table_name: "records".to_string(),
            fields: vec![
                plain_field("userId", FieldKind::String),
                plain_field("name", FieldKind::String),
                ref_field("departmentId", "hr.departments", "name", true),
            ],
            list_views: vec![],
            workflow: None,
        })
        .unwrap();
    registry
        .register(EntityDefinition {
            name: "helpdesk.tickets".to_string(),
            label: "Ticket".to_string(),
            table_name: "records".to_string(),
            fields: vec![
                plain_field("title", FieldKind::String),
                plain_field("description", FieldKind::String),
                {
                    let mut f = plain_field("status", FieldKind::Enum);
                    f.enum_values = Some(vec![
                        "open".to_string(),
                        "in_progress".to_string(),
                        "resolved".to_string(),
                        "closed".to_string(),
                    ]);
                    f
                },
                {
                    let mut f = plain_field("priority", FieldKind::Enum);
                    f.enum_values = Some(vec![
                        "low".to_string(),
                        "medium".to_string(),
                        "high".to_string(),
                        "urgent".to_string(),
                    ]);
                    f
                },
                ref_field("assigneeId", "hr.employees", "name", true),
                ref_field("departmentId", "hr.departments", "name", true),
            ],
            list_views: vec![metap_metadata::EntityListView {
                name: "default".to_string(),
                label: "Default".to_string(),
                fields: vec!["title".to_string(), "status".to_string()],
                filters: vec![
                    "status".to_string(),
                    "assigneeId".to_string(),
                    "departmentId".to_string(),
                ],
                default_sort: Some("-createdAt".to_string()),
                max_limit: 100,
            }],
            workflow: None,
        })
        .unwrap();

    let permissions = Arc::new(PermissionService::new(Box::new(PostgresPolicyStore::new(test_router(
        pool.clone(),
    )))));
    let crud = Arc::new(CrudService::new(
        test_router(pool.clone()),
        std::sync::Arc::new(arc_swap::ArcSwap::new(std::sync::Arc::new(registry))),
        permissions,
    ));

    eprintln!(
        "sustained_concurrent_list: {CONCURRENCY} concurrent workers, {DURATION_SECS}s, against \
         500K helpdesk.tickets / 200 hr.employees / 20 hr.departments (real, related data)"
    );

    let report = run_sustained_load(
        "sustained_concurrent_list",
        LoadTestConfig {
            duration: Duration::from_secs(DURATION_SECS),
            concurrency: CONCURRENCY,
        },
        move |worker, i| {
            let crud = crud.clone();
            let dept_ids = dept_ids.clone();
            async move {
                let dept = dept_ids[(worker + i) % dept_ids.len()];
                let mut ctx_attrs = serde_json::Map::new();
                ctx_attrs.insert("departmentId".to_string(), json!(dept));
                let ctx = RequestContext {
                    tenant_id: tenant_id.to_string(),
                    user_id: Some(Uuid::new_v4().to_string()),
                    roles: Some(vec!["employee".to_string()]),
                    function_id: None,
                    context_attributes: Some(ctx_attrs),
                };
                let input = ListInput {
                    limit: 50,
                    ..Default::default()
                };
                match crud.list("helpdesk.tickets", &input, &ctx).await {
                    Ok(ServiceResult::Ok { .. }) => Ok(()),
                    other => Err(anyhow::anyhow!("unexpected result: {other:?}")),
                }
            }
        },
    )
    .await;

    report.print_summary();
    report.assert_no_errors();
}

/// Wider/bigger sibling of the test above — found missing scope in code review, 2026-08-23:
/// "different entities, different tenants, lookups joining many tables" (the earlier test used
/// one tenant, two `Reference` fields). This one: **10 different tenants**, each with its own
/// 20 departments / 200 employees / 1,000,000 tickets (**10M tickets total**, at/past the
/// documented `@10M/entity` table-per-entity trigger, `docs/architectures/09-adr.md`) — sharing
/// one physical `records` table, exactly the shared-table scenario that trigger is about.
/// `helpdesk.tickets` here has **three** `Reference` fields (`assigneeId`, `reporterId`, both
/// -> `hr.employees`, plus `departmentId`), so `hydrate_related_display` runs 3 separate
/// permission-check + batch-fetch + record-condition passes per `list()` call, not 2 — closer
/// to what a real multi-way "join" of business data looks like within this platform's
/// no-SQL-JOIN constraint (`docs/features/05-cross-entity-relations.md`'s Mode 3 gap). Each
/// worker picks a random *(tenant, department)* pair per iteration, so the concurrent traffic
/// is genuinely mixed across tenants, not all hammering one tenant_id value.
///
/// Needs the out-of-band 10-tenant seed (10x the single-tenant seed this file's other
/// sustained test uses) — see the benchmarking session, not part of a normal e2e run.
#[tokio::test]
#[ignore = "manual benchmark: requires the out-of-band 10-tenant/10M-row seed, not part of a normal e2e run"]
async fn sustained_concurrent_list_across_many_tenants_at_ten_million_rows() {
    use std::sync::Arc;
    use std::time::Duration;

    use support::{run_sustained_load, LoadTestConfig};

    const DURATION_SECS: u64 = 600;
    const CONCURRENCY: usize = 20;

    let database_url = std::env::var("DATABASE_URL").expect("DATABASE_URL required for this e2e test");
    let pool = PgPoolOptions::new()
        .max_connections(CONCURRENCY as u32 + 5)
        .connect(&database_url)
        .await
        .unwrap();

    // (tenant_id, department_id) pairs across all 10 seeded tenants — a worker picks one at
    // random each iteration, so traffic is genuinely mixed across tenants, not one tenant_id
    // repeated with different departments.
    let tenant_dept_pairs: Vec<(Uuid, Uuid)> =
        sqlx::query_as("SELECT tenant_id, id FROM records WHERE entity = 'hr.departments' ORDER BY tenant_id, id")
            .fetch_all(&pool)
            .await
            .expect("hr.departments must already be seeded (out-of-band, 10 tenants) before this test runs");
    let distinct_tenants: std::collections::HashSet<Uuid> = tenant_dept_pairs.iter().map(|(t, _)| *t).collect();
    assert!(
        distinct_tenants.len() >= 2,
        "expected multiple tenants seeded (got {}) — this test is specifically about cross-tenant \
         concurrent traffic, not a single tenant",
        distinct_tenants.len()
    );
    eprintln!(
        "sustained_concurrent_list (multi-tenant): {} tenants, {} department pairs",
        distinct_tenants.len(),
        tenant_dept_pairs.len()
    );

    fn ref_field(name: &str, ref_entity: &str, ref_display_field: &str, indexed: bool) -> EntityField {
        EntityField {
            name: name.to_string(),
            label: name.to_string(),
            kind: FieldKind::Reference,
            required: None,
            indexed: indexed.then_some(true),
            unique: None,
            enum_values: None,
            ref_entity: Some(ref_entity.to_string()),
            ref_display_field: Some(ref_display_field.to_string()),
            searchable: None,
            search_mode: None,
            sortable: None,
            storage: None,
        }
    }
    fn plain_field(name: &str, kind: FieldKind) -> EntityField {
        EntityField {
            name: name.to_string(),
            label: name.to_string(),
            kind,
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
        }
    }

    let mut registry = MetadataRegistry::new();
    registry
        .register(EntityDefinition {
            name: "hr.departments".to_string(),
            label: "Department".to_string(),
            table_name: "records".to_string(),
            fields: vec![plain_field("name", FieldKind::String)],
            list_views: vec![],
            workflow: None,
        })
        .unwrap();
    registry
        .register(EntityDefinition {
            name: "hr.employees".to_string(),
            label: "Employee".to_string(),
            table_name: "records".to_string(),
            fields: vec![
                plain_field("userId", FieldKind::String),
                plain_field("name", FieldKind::String),
                ref_field("departmentId", "hr.departments", "name", true),
            ],
            list_views: vec![],
            workflow: None,
        })
        .unwrap();
    registry
        .register(EntityDefinition {
            name: "helpdesk.tickets".to_string(),
            label: "Ticket".to_string(),
            table_name: "records".to_string(),
            fields: vec![
                plain_field("title", FieldKind::String),
                plain_field("description", FieldKind::String),
                {
                    let mut f = plain_field("status", FieldKind::Enum);
                    f.enum_values = Some(vec![
                        "open".to_string(),
                        "in_progress".to_string(),
                        "resolved".to_string(),
                        "closed".to_string(),
                    ]);
                    f
                },
                {
                    let mut f = plain_field("priority", FieldKind::Enum);
                    f.enum_values = Some(vec![
                        "low".to_string(),
                        "medium".to_string(),
                        "high".to_string(),
                        "urgent".to_string(),
                    ]);
                    f
                },
                ref_field("assigneeId", "hr.employees", "name", true),
                ref_field("reporterId", "hr.employees", "name", true),
                ref_field("departmentId", "hr.departments", "name", true),
            ],
            list_views: vec![metap_metadata::EntityListView {
                name: "default".to_string(),
                label: "Default".to_string(),
                fields: vec!["title".to_string(), "status".to_string()],
                filters: vec![
                    "status".to_string(),
                    "assigneeId".to_string(),
                    "reporterId".to_string(),
                    "departmentId".to_string(),
                ],
                default_sort: Some("-createdAt".to_string()),
                max_limit: 100,
            }],
            workflow: None,
        })
        .unwrap();

    let permissions = Arc::new(PermissionService::new(Box::new(PostgresPolicyStore::new(test_router(
        pool.clone(),
    )))));
    let crud = Arc::new(CrudService::new(
        test_router(pool.clone()),
        std::sync::Arc::new(arc_swap::ArcSwap::new(std::sync::Arc::new(registry))),
        permissions,
    ));

    eprintln!(
        "sustained_concurrent_list (multi-tenant): {CONCURRENCY} concurrent workers, {DURATION_SECS}s, \
         against 10M helpdesk.tickets / 2000 hr.employees / 200 hr.departments across 10 tenants"
    );

    let report = run_sustained_load(
        "sustained_concurrent_list (multi-tenant, 10M)",
        LoadTestConfig {
            duration: Duration::from_secs(DURATION_SECS),
            concurrency: CONCURRENCY,
        },
        move |worker, i| {
            let crud = crud.clone();
            let pairs = tenant_dept_pairs.clone();
            async move {
                let (tenant_id, dept) = pairs[(worker * 7 + i) % pairs.len()];
                let mut ctx_attrs = serde_json::Map::new();
                ctx_attrs.insert("departmentId".to_string(), json!(dept));
                let ctx = RequestContext {
                    tenant_id: tenant_id.to_string(),
                    user_id: Some(Uuid::new_v4().to_string()),
                    roles: Some(vec!["employee".to_string()]),
                    function_id: None,
                    context_attributes: Some(ctx_attrs),
                };
                let input = ListInput {
                    limit: 50,
                    ..Default::default()
                };
                match crud.list("helpdesk.tickets", &input, &ctx).await {
                    Ok(ServiceResult::Ok { .. }) => Ok(()),
                    other => Err(anyhow::anyhow!("unexpected result: {other:?}")),
                }
            }
        },
    )
    .await;

    report.print_summary();
    report.assert_no_errors();
}

/// Security regression suite (`testing/security/checklist.md`) — the application-level
/// counterpart to `metap-control/tests/tenant_isolation_postgres.rs`'s connection-level check.
/// `crates/metap-control` proves `Router::begin` never leaks a *schema* between tenants sharing
/// one physical connection; this proves `CrudService::list` never leaks *rows* between tenants
/// sharing one *pool connection*, forced via a deliberately small pool (`max_connections(2)`)
/// under real concurrency — the actual shape of the past "cross-tenant leak" this repo already
/// fixed once (commit `cc5f1ea`), where the bug was a missing `tenant_id` filter in application
/// SQL, not a schema-routing gap. Every single response, across many interleaved concurrent
/// calls for two different tenants, must contain *only* that call's own tenant's records.
#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn concurrent_cross_tenant_list_calls_never_return_another_tenants_records() {
    let pool = PgPoolOptions::new()
        .max_connections(2)
        .connect(&std::env::var("DATABASE_URL").expect("DATABASE_URL required for this e2e test"))
        .await
        .unwrap();
    let tenant_a = Uuid::new_v4();
    let tenant_b = Uuid::new_v4();

    let mut registry = MetadataRegistry::new();
    registry.register(test_entity()).unwrap();
    let permissions = std::sync::Arc::new(PermissionService::new(Box::new(PostgresPolicyStore::new(test_router(
        pool.clone(),
    )))));
    let crud = std::sync::Arc::new(CrudService::new(
        test_router(pool.clone()),
        std::sync::Arc::new(arc_swap::ArcSwap::new(std::sync::Arc::new(registry))),
        permissions,
    ));

    for (tenant_id, prefix) in [(tenant_a, "secret-a"), (tenant_b, "secret-b")] {
        let ctx = admin_context(tenant_id);
        for i in 0..5 {
            let mut payload = JsonObject::new();
            payload.insert("name".to_string(), json!(format!("{prefix}-{i}")));
            crud.create("test.orders", &payload, &ctx).await.unwrap();
        }
    }

    let mut handles = Vec::new();
    for i in 0..30 {
        let crud = crud.clone();
        // Alternate which tenant each concurrent call belongs to — the interleaving itself is
        // what stresses connection reuse across tenants, not just running many calls for one.
        let (tenant_id, expected_prefix, forbidden_prefix) = if i % 2 == 0 {
            (tenant_a, "secret-a", "secret-b")
        } else {
            (tenant_b, "secret-b", "secret-a")
        };
        handles.push(tokio::spawn(async move {
            let ctx = admin_context(tenant_id);
            let input = ListInput {
                limit: 50,
                ..Default::default()
            };
            let result = crud.list("test.orders", &input, &ctx).await.unwrap();
            let ServiceResult::Ok { data, .. } = result else {
                panic!("expected list to succeed");
            };
            for record in &data {
                let name = record.data.get("name").and_then(|v| v.as_str()).unwrap_or_default();
                assert!(
                    name.starts_with(expected_prefix),
                    "tenant {tenant_id} saw a record it doesn't own: {name:?}"
                );
                assert!(
                    !name.starts_with(forbidden_prefix),
                    "tenant {tenant_id} leaked another tenant's record: {name:?}"
                );
            }
            assert_eq!(
                data.len(),
                5,
                "tenant {tenant_id} must see exactly its own 5 records, no more, no less"
            );
        }));
    }
    for h in handles {
        h.await.unwrap();
    }

    cleanup(&pool, tenant_a).await;
    cleanup(&pool, tenant_b).await;
}

/// Performance pillar (`testing/README.md`) — the write-path counterpart to the two
/// `list()`-only sustained tests above. Each iteration runs the full
/// create → update → transition → delete cycle `full_lifecycle_create_get_update_transition_delete`
/// exercises for correctness, but as a sustained concurrent load rather than a single sequential
/// run — `test_entity()` (`test.orders`) is the only fixture in this file with a real workflow,
/// so it's the only one that can drive a `transition()` call under load too. Shorter default
/// duration than the two `list()` benchmarks (60s, not 600s) — four round trips per iteration
/// is inherently heavier than one `list()` call, and this exists to prove out the write path's
/// shape/harness reuse, not to be a from-scratch capacity benchmark; bump `DURATION_SECS` for a
/// deeper run.
#[tokio::test]
#[ignore = "manual benchmark: writes real records, not part of a normal e2e run"]
async fn sustained_concurrent_create_update_transition_delete_cycle() {
    use std::time::Duration;

    use support::{run_sustained_load, LoadTestConfig};

    const DURATION_SECS: u64 = 60;
    const CONCURRENCY: usize = 10;

    let database_url = std::env::var("DATABASE_URL").expect("DATABASE_URL required for this e2e test");
    let pool = PgPoolOptions::new()
        .max_connections(CONCURRENCY as u32 + 5)
        .connect(&database_url)
        .await
        .unwrap();
    let tenant_id = Uuid::new_v4();

    let mut registry = MetadataRegistry::new();
    registry.register(test_entity()).unwrap();
    let permissions = std::sync::Arc::new(PermissionService::new(Box::new(PostgresPolicyStore::new(test_router(
        pool.clone(),
    )))));
    let crud = std::sync::Arc::new(CrudService::new(
        test_router(pool.clone()),
        std::sync::Arc::new(arc_swap::ArcSwap::new(std::sync::Arc::new(registry))),
        permissions,
    ));

    eprintln!(
        "sustained_concurrent_create_update_transition_delete_cycle: {CONCURRENCY} concurrent \
         workers, {DURATION_SECS}s, one full create+update+transition+delete cycle per iteration"
    );

    let report = run_sustained_load(
        "sustained_concurrent_create_update_transition_delete_cycle",
        LoadTestConfig {
            duration: Duration::from_secs(DURATION_SECS),
            concurrency: CONCURRENCY,
        },
        move |worker, i| {
            let crud = crud.clone();
            let ctx = admin_context(tenant_id);
            async move {
                let mut payload = JsonObject::new();
                payload.insert("name".to_string(), json!(format!("load-{worker}-{i}")));
                // amount == 100 so the "approve" transition's guard passes on the first try.
                payload.insert("amount".to_string(), json!(100));
                let created = match crud.create("test.orders", &payload, &ctx).await? {
                    ServiceResult::Ok { data, .. } => data,
                    other => anyhow::bail!("create failed: {other:?}"),
                };

                let mut update_payload = JsonObject::new();
                update_payload.insert("name".to_string(), json!(format!("load-{worker}-{i}-updated")));
                let updated = match crud
                    .update("test.orders", created.id, created.version, &update_payload, &ctx)
                    .await?
                {
                    ServiceResult::Ok { data, .. } => data,
                    other => anyhow::bail!("update failed: {other:?}"),
                };

                let transitioned = match crud
                    .transition("test.orders", created.id, "approve", updated.version, None, &ctx)
                    .await?
                {
                    ServiceResult::Ok { data, .. } => data,
                    other => anyhow::bail!("transition failed: {other:?}"),
                };

                match crud
                    .delete("test.orders", created.id, transitioned.version, &ctx)
                    .await?
                {
                    ServiceResult::Ok { .. } => Ok(()),
                    other => anyhow::bail!("delete failed: {other:?}"),
                }
            }
        },
    )
    .await;

    report.print_summary();
    report.assert_no_errors();

    cleanup(&pool, tenant_id).await;
}
