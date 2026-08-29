//! E2E test: a real tonic server, bound to a real socket, hit with a real tonic client over a
//! real network stack — auth included (a real RS256 JWT, minted and verified, not stubbed).
//! `#[ignore]`d, same convention as `crates/metap-http/tests/http_server.rs` (needs a running
//! Postgres, run explicitly via `cargo test -- --ignored`).

use std::process::Command;
use std::sync::Arc;
use std::time::Duration;

use arc_swap::ArcSwap;
use jsonwebtoken::{DecodingKey, EncodingKey};
use metap_control::PostgresPolicyStore;
use metap_crud::CrudService;
use metap_grpc::pb::record_service_client::RecordServiceClient;
use metap_grpc::pb::{CreateRequest, DeleteRequest, GetRequest, ListRequest, TransitionRequest, UpdateRequest};
use metap_grpc::{AuthConfig, GrpcRecordService, TokenVerifier};
use metap_metadata::{
    EntityDefinition, EntityField, EntityListView, EntityWorkflow, FieldKind, MetadataRegistry, WorkflowTransition,
};
use metap_permission::PermissionService;
use serde_json::json;
use sqlx::postgres::PgPoolOptions;
use sqlx::PgPool;
use tonic::Request;
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
        let path = std::env::temp_dir().join(format!("metap-grpc-test-{}", Uuid::new_v4()));
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
        name: "test.grpc_orders".to_string(),
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

async fn connect_db() -> PgPool {
    let database_url = std::env::var("DATABASE_URL").expect("DATABASE_URL required for this e2e test");
    PgPoolOptions::new()
        .max_connections(5)
        .connect(&database_url)
        .await
        .unwrap()
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn full_grpc_lifecycle_over_a_real_server_and_a_real_jwt() {
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
    let token = metap_peripherals::mint_jwt(&private_pem, tenant_id, user_id, 3600).unwrap();
    let decoding_key = DecodingKey::from_rsa_pem(public_pem.as_bytes()).unwrap();
    let _ = EncodingKey::from_rsa_pem(private_pem.as_bytes()).unwrap(); // exercised via mint_jwt above

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
    let service = GrpcRecordService::new(crud, auth);

    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = listener.local_addr().unwrap();
    drop(listener); // tonic's `serve` binds its own listener from the address
    tokio::spawn(metap_grpc::serve(addr, service, None));
    // Give the server a moment to start listening before the client dials.
    tokio::time::sleep(Duration::from_millis(200)).await;

    let mut client = RecordServiceClient::connect(format!("http://{addr}")).await.unwrap();

    fn authed<T>(msg: T, token: &str) -> Request<T> {
        let mut req = Request::new(msg);
        req.metadata_mut()
            .insert("authorization", format!("Bearer {token}").parse().unwrap());
        req
    }

    // unauthenticated call is rejected
    let unauthed = client
        .get(Request::new(GetRequest {
            entity_name: "test.grpc_orders".to_string(),
            id: Uuid::new_v4().to_string(),
        }))
        .await;
    assert_eq!(unauthed.unwrap_err().code(), tonic::Code::Unauthenticated);

    // create
    let created = client
        .create(authed(
            CreateRequest {
                entity_name: "test.grpc_orders".to_string(),
                data: Some(metap_grpc::convert::json_to_struct(json!({ "name": "First" }))),
            },
            &token,
        ))
        .await
        .unwrap()
        .into_inner();
    let record = metap_grpc::convert::struct_to_json(created.record.unwrap());
    assert_eq!(record["status"], "draft");
    let id = record["id"].as_str().unwrap().to_string();
    let version = record["version"].as_i64().unwrap() as i32;

    // get
    let fetched = client
        .get(authed(
            GetRequest {
                entity_name: "test.grpc_orders".to_string(),
                id: id.clone(),
            },
            &token,
        ))
        .await
        .unwrap()
        .into_inner();
    assert_eq!(metap_grpc::convert::struct_to_json(fetched.record.unwrap())["id"], id);

    // transition
    let transitioned = client
        .transition(authed(
            TransitionRequest {
                entity_name: "test.grpc_orders".to_string(),
                id: id.clone(),
                action: "activate".to_string(),
                expected_version: version,
                data: None,
            },
            &token,
        ))
        .await
        .unwrap()
        .into_inner();
    let transitioned_record = metap_grpc::convert::struct_to_json(transitioned.record.unwrap());
    assert_eq!(transitioned_record["status"], "active");
    let version = transitioned_record["version"].as_i64().unwrap() as i32;

    // update with a stale version is rejected as a version conflict
    let stale_update = client
        .update(authed(
            UpdateRequest {
                entity_name: "test.grpc_orders".to_string(),
                id: id.clone(),
                expected_version: version - 1,
                data: Some(metap_grpc::convert::json_to_struct(json!({ "name": "Stale" }))),
            },
            &token,
        ))
        .await;
    assert_eq!(stale_update.unwrap_err().code(), tonic::Code::Aborted);

    // update with the correct version succeeds
    let updated = client
        .update(authed(
            UpdateRequest {
                entity_name: "test.grpc_orders".to_string(),
                id: id.clone(),
                expected_version: version,
                data: Some(metap_grpc::convert::json_to_struct(json!({ "name": "Renamed" }))),
            },
            &token,
        ))
        .await
        .unwrap()
        .into_inner();
    let updated_record = metap_grpc::convert::struct_to_json(updated.record.unwrap());
    assert_eq!(updated_record["data"]["name"], "Renamed");
    let version = updated_record["version"].as_i64().unwrap() as i32;

    // list finds the record
    let listed = client
        .list(authed(
            ListRequest {
                entity_name: "test.grpc_orders".to_string(),
                query: None,
            },
            &token,
        ))
        .await
        .unwrap()
        .into_inner();
    assert!(listed
        .records
        .into_iter()
        .any(|r| metap_grpc::convert::struct_to_json(r)["id"] == id));

    // delete
    client
        .delete(authed(
            DeleteRequest {
                entity_name: "test.grpc_orders".to_string(),
                id: id.clone(),
                expected_version: version,
            },
            &token,
        ))
        .await
        .unwrap();

    let after_delete = client
        .get(authed(
            GetRequest {
                entity_name: "test.grpc_orders".to_string(),
                id,
            },
            &token,
        ))
        .await;
    assert_eq!(after_delete.unwrap_err().code(), tonic::Code::NotFound);
}
