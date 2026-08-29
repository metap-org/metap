//! E2E test for `GrpcBackend` specifically — a real tonic server (`GrpcRecordService`, exactly
//! as `apps/jira-server`/`apps/crm-server` would run it) hit through the `RecordBackend` trait
//! object via `GrpcBackend`, not through raw `RecordServiceClient` calls
//! (`grpc_crud_postgres.rs` already covers the server side end to end). This is the piece the
//! BFF gateway (`crates/graphql-gateway`) actually depends on: a `GrpcBackend` behind
//! `Arc<dyn RecordBackend>` must behave identically to an in-process `CrudService` for every
//! `RecordBackend` method. `#[ignore]`d, same convention as this crate's other e2e test (needs a
//! running Postgres, run explicitly via `cargo test -- --ignored`).

use std::process::Command;
use std::sync::Arc;
use std::time::Duration;

use arc_swap::ArcSwap;
use jsonwebtoken::DecodingKey;
use metap_control::PostgresPolicyStore;
use metap_crud::{CrudService, RecordBackend, ServiceResult};
use metap_grpc::{AuthConfig, GrpcBackend, GrpcRecordService, TokenVerifier};
use metap_metadata::{
    EntityDefinition, EntityField, EntityListView, EntityWorkflow, FieldKind, MetadataRegistry, WorkflowTransition,
};
use metap_permission::{PermissionService, RequestContext};
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

fn tempdir() -> TempDir {
    TempDir::new()
}

struct TempDir(std::path::PathBuf);

impl TempDir {
    fn new() -> Self {
        let path = std::env::temp_dir().join(format!("metap-grpc-backend-test-{}", Uuid::new_v4()));
        std::fs::create_dir_all(&path).unwrap();
        Self(path)
    }

    fn path(&self) -> &std::path::Path {
        &self.0
    }
}

fn openssl_genrsa(dir: &std::path::Path) -> (String, String) {
    let private_path = dir.join("private.pem");
    let public_path = dir.join("public.pem");

    let status = Command::new("openssl")
        .args(["genrsa", "-out"])
        .arg(&private_path)
        .arg("2048")
        .status()
        .expect("openssl genrsa must run for this e2e test");
    assert!(status.success());

    let status = Command::new("openssl")
        .args(["rsa", "-in"])
        .arg(&private_path)
        .args(["-pubout", "-out"])
        .arg(&public_path)
        .status()
        .expect("openssl rsa -pubout must run for this e2e test");
    assert!(status.success());

    (
        std::fs::read_to_string(private_path).unwrap(),
        std::fs::read_to_string(public_path).unwrap(),
    )
}

fn test_entity() -> EntityDefinition {
    EntityDefinition {
        name: "test.grpc_backend_orders".to_string(),
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
                min: None,
                max: None,
                min_length: None,
                max_length: None,
            },
            EntityField {
                name: "status".to_string(),
                label: "Status".to_string(),
                kind: FieldKind::Enum,
                required: None,
                indexed: None,
                unique: None,
                enum_values: Some(vec!["draft".to_string(), "active".to_string()]),
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
            fields: vec!["name".to_string()],
            filters: vec![],
            default_sort: Some("-createdAt".to_string()),
            max_limit: 50,
        }],
        workflow: Some(EntityWorkflow {
            state_field: "status".to_string(),
            initial_state: "draft".to_string(),
            terminal_states: vec![],
            transitions: vec![WorkflowTransition {
                action: "activate".to_string(),
                from: "draft".to_string(),
                to: "active".to_string(),
                label: "Activate".to_string(),
                guard: None,
                validator: None,
                set_fields: None,
            }],
        }),
    }
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

async fn connect_db() -> PgPool {
    let database_url = std::env::var("DATABASE_URL").expect("DATABASE_URL required for this e2e test");
    PgPoolOptions::new()
        .max_connections(5)
        .connect(&database_url)
        .await
        .unwrap()
}

fn unwrap_ok<T>(result: ServiceResult<T>) -> T {
    match result {
        ServiceResult::Ok { data, .. } => data,
        ServiceResult::Err {
            status, error, message, ..
        } => {
            panic!("expected Ok, got {status} {error}: {message:?}")
        }
    }
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn grpc_backend_full_lifecycle_matches_direct_crud_service_behavior() {
    let pool = connect_db().await;
    let tenant_id = Uuid::new_v4();
    let user_id = Uuid::new_v4();

    sqlx::query("INSERT INTO user_roles (tenant_id, user_id, role) VALUES ($1, $2, 'admin')")
        .bind(tenant_id)
        .bind(user_id)
        .execute(&pool)
        .await
        .unwrap();

    let keydir = tempdir();
    let (private_pem, public_pem) = openssl_genrsa(keydir.path());
    // The gateway's own static, pre-minted per-upstream service JWT — matches this suite's
    // `CRON_SERVICE_JWT` precedent, not a token tied to any particular end-user request.
    let service_jwt = metap_peripherals::mint_jwt(&private_pem, tenant_id, user_id, 3600).unwrap();
    let decoding_key = DecodingKey::from_rsa_pem(public_pem.as_bytes()).unwrap();

    let mut registry = MetadataRegistry::new();
    registry.register(test_entity()).unwrap();
    let registry = Arc::new(registry);
    let permissions = Arc::new(PermissionService::new(Box::new(PostgresPolicyStore::new(test_router(
        pool.clone(),
    )))));
    let crud = Arc::new(CrudService::new(
        test_router(pool.clone()),
        Arc::new(ArcSwap::new(registry)),
        permissions,
    ));

    let auth = AuthConfig {
        verifier: TokenVerifier::Static {
            decoding_key,
            leeway: 20,
        },
        router: test_router(pool.clone()),
        auth_context_entity: None,
        context_attributes_cache: metap_control::ContextAttributesCache::new(Duration::from_secs(60)),
    };
    let service = GrpcRecordService::new(crud.clone(), auth);

    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = listener.local_addr().unwrap();
    drop(listener); // tonic's `serve` binds its own listener from the address
    tokio::spawn(metap_grpc::serve(addr, service, None));
    tokio::time::sleep(Duration::from_millis(200)).await;

    let backend = GrpcBackend::connect(format!("http://{addr}"), service_jwt.clone())
        .await
        .unwrap();
    let ctx = admin_context(tenant_id); // ignored by GrpcBackend itself — identity comes from service_jwt

    // create
    let created = unwrap_ok(
        backend
            .create(
                "test.grpc_backend_orders",
                &json!({ "name": "First" }).as_object().unwrap().clone(),
                &ctx,
            )
            .await
            .unwrap(),
    );
    assert_eq!(created.status.as_deref(), Some("draft"));
    assert_eq!(created.data["name"], "First");
    let id = created.id;
    let version = created.version;

    // get — compares against a direct CrudService call for the exact same record
    let (fetched, capabilities) = unwrap_ok(backend.get("test.grpc_backend_orders", id, &ctx).await.unwrap());
    assert_eq!(fetched.id, id);
    assert!(capabilities.can_update);
    let (direct_fetched, _) = unwrap_ok(crud.get("test.grpc_backend_orders", id, &ctx).await.unwrap());
    assert_eq!(fetched.data, direct_fetched.data);

    // get_many — no batch RPC on the wire, implemented as N `get`s; still returns the right shape
    let batch = unwrap_ok(
        backend
            .get_many("test.grpc_backend_orders", &[id, Uuid::new_v4()], &ctx)
            .await
            .unwrap(),
    );
    assert_eq!(
        batch.len(),
        1,
        "the nonexistent id must simply be absent, not error the whole batch"
    );
    assert_eq!(batch[0].0, id);

    // transition
    let transitioned = unwrap_ok(
        backend
            .transition("test.grpc_backend_orders", id, "activate", version, None, &ctx)
            .await
            .unwrap(),
    );
    assert_eq!(transitioned.status.as_deref(), Some("active"));
    let version = transitioned.version;

    // update with a stale version surfaces as a 409 ServiceResult::Err, not an anyhow error
    let stale_update = backend
        .update(
            "test.grpc_backend_orders",
            id,
            version - 1,
            &json!({ "name": "Stale" }).as_object().unwrap().clone(),
            &ctx,
        )
        .await
        .unwrap();
    match stale_update {
        ServiceResult::Err { status, .. } => assert_eq!(status, 409),
        ServiceResult::Ok { .. } => panic!("expected a version-conflict error"),
    }

    // update with the correct version succeeds
    let updated = unwrap_ok(
        backend
            .update(
                "test.grpc_backend_orders",
                id,
                version,
                &json!({ "name": "Renamed" }).as_object().unwrap().clone(),
                &ctx,
            )
            .await
            .unwrap(),
    );
    assert_eq!(updated.data["name"], "Renamed");
    let version = updated.version;

    // list finds the record
    let listed = unwrap_ok(
        backend
            .list(
                "test.grpc_backend_orders",
                &metap_query::ListInput {
                    limit: 30,
                    sort: None,
                    filters: vec![],
                    cursor: None,
                    list_view: None,
                    jql: None,
                },
                &ctx,
            )
            .await
            .unwrap(),
    );
    assert!(listed.iter().any(|r| r.id == id));

    // delete
    unwrap_ok(
        backend
            .delete("test.grpc_backend_orders", id, version, &ctx)
            .await
            .unwrap(),
    );

    let after_delete = backend.get("test.grpc_backend_orders", id, &ctx).await.unwrap();
    match after_delete {
        ServiceResult::Err { status, .. } => assert_eq!(status, 404),
        ServiceResult::Ok { .. } => panic!("expected the record to be gone after delete"),
    }
}
