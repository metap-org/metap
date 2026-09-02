//! The critical e2e test for this whole crate: proves this is a *real* BFF, not two services
//! glued together at the frontend. Spins up two independent, real service harnesses (each with
//! its own tenant, own keypair, own real Postgres-backed `CrudService`, own real gRPC listener,
//! own real REST `/metadata/entities` listener — exactly the shape `../metap-demo-jira`/
//! `../metap-demo-crm` actually run), points `graphql_gateway::schema_builder::build` at both (the
//! same discovery path the real binary's boot sequence uses), then executes **one** GraphQL
//! query that reads a field from each harness's entity and asserts both are present in the
//! single response. `#[ignore]`d, same convention as this workspace's other e2e tests (needs a
//! running Postgres, run explicitly via `cargo test -- --ignored`).

use std::process::Command;
use std::sync::Arc;

use arc_swap::ArcSwap;
use jsonwebtoken::DecodingKey;
use metap_control::PostgresPolicyStore;
use metap_crud::CrudService;
use metap_graphql_gateway::{config::UpstreamConfig, schema_builder};
use metap_metadata::{EntityDefinition, EntityField, EntityListView, FieldKind, MetadataRegistry};
use metap_permission::{PermissionService, RequestContext};
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

struct TempDir(std::path::PathBuf);

impl TempDir {
    fn new() -> Self {
        let path = std::env::temp_dir().join(format!("metap-graphql-gateway-test-{}", Uuid::new_v4()));
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

fn simple_entity(name: &str, label: &str) -> EntityDefinition {
    EntityDefinition {
        name: name.to_string(),
        label: label.to_string(),
        table_name: "records".to_string(),
        fields: vec![EntityField {
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
            fields: vec!["name".to_string()],
            filters: vec![],
            default_sort: None,
            max_limit: 50,
        }],
        workflow: None,
    }
}

fn admin_context(tenant_id: Uuid, user_id: Uuid) -> RequestContext {
    RequestContext {
        tenant_id: tenant_id.to_string(),
        user_id: Some(user_id.to_string()),
        roles: Some(vec!["admin".to_string()]),
        function_id: None,
        context_attributes: None,
        forwarded_bearer_token: None,
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

/// A real, independent "upstream microservice" — same shape `../metap-demo-jira`/`../metap-demo-crm`
/// actually boot: its own tenant, own RS256 keypair, own real Postgres-backed `CrudService`
/// behind a real gRPC listener, and its own real REST listener serving `GET /metadata/entities`
/// (`metap_http::build_router`, exactly what the real binaries mount).
struct Harness {
    upstream: UpstreamConfig,
    crud: Arc<CrudService>,
    tenant_id: Uuid,
    user_id: Uuid,
}

async fn spin_up_harness(pool: PgPool, name: &str, entity: EntityDefinition) -> Harness {
    let tenant_id = Uuid::new_v4();
    // A real user (email+password) rather than a hand-minted JWT — this is what
    // `ServiceTokenSource::start` (called from `schema_builder::build`, exactly like the real
    // binary) will log in as through this harness's own `/auth/login`, proving the login-based
    // service-account flow works end to end, not just `GrpcBackend`'s dispatch.
    let service_email = format!("service-{tenant_id}@test.local");
    let service_password = "test-password-not-a-real-secret";
    let user = metap_peripherals::create_user(&pool, tenant_id, &service_email, service_password)
        .await
        .unwrap();
    let user_id = user.id;

    sqlx::query("INSERT INTO user_roles (tenant_id, user_id, role) VALUES ($1, $2, 'admin')")
        .bind(tenant_id)
        .bind(user_id)
        .execute(&pool)
        .await
        .unwrap();

    let keydir = TempDir::new();
    let (private_pem, public_pem) = openssl_genrsa(keydir.path());
    let decoding_key = DecodingKey::from_rsa_pem(public_pem.as_bytes()).unwrap();

    let mut registry = MetadataRegistry::new();
    registry.register(entity).unwrap();
    let registry = Arc::new(registry);
    let permissions = Arc::new(PermissionService::new(Box::new(PostgresPolicyStore::new(test_router(
        pool.clone(),
    )))));
    let crud = Arc::new(CrudService::new(
        test_router(pool.clone()),
        Arc::new(ArcSwap::new(registry.clone())),
        permissions.clone(),
    ));

    // Real gRPC listener — same `metap_grpc::serve` the real binaries spawn.
    let grpc_auth = metap_grpc::AuthConfig {
        verifier: metap_grpc::TokenVerifier::Static {
            decoding_key: decoding_key.clone(),
            leeway: 20,
        },
        router: test_router(pool.clone()),
        auth_context_entity: None,
        context_attributes_cache: metap_control::ContextAttributesCache::new(std::time::Duration::from_secs(60)),
    };
    let grpc_service = metap_grpc::GrpcRecordService::new(crud.clone(), grpc_auth);
    let grpc_listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let grpc_addr = grpc_listener.local_addr().unwrap();
    drop(grpc_listener);
    tokio::spawn(metap_grpc::serve(grpc_addr, grpc_service, None));

    // Real REST listener — just `metap_http::build_router`, exactly what the real binaries
    // mount `GET /metadata/entities` through (no gateway-specific route exists on this side).
    let state = metap_http::AppState::new(
        pool.clone(),
        registry.clone(),
        Arc::new(ArcSwap::new(registry)),
        permissions,
        decoding_key,
        private_pem,
        test_router(pool.clone()),
    );
    let rest_router = metap_http::build_router(state, &[], axum::Router::new());
    let rest_listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let rest_addr = rest_listener.local_addr().unwrap();
    tokio::spawn(async move {
        axum::serve(
            rest_listener,
            rest_router.into_make_service_with_connect_info::<std::net::SocketAddr>(),
        )
        .await
        .unwrap();
    });

    tokio::time::sleep(std::time::Duration::from_millis(200)).await;

    Harness {
        upstream: UpstreamConfig {
            name: name.to_string(),
            grpc_addr: format!("http://{grpc_addr}"),
            metadata_url: format!("http://{rest_addr}/metadata/entities"),
            login_url: format!("http://{rest_addr}/auth/login"),
            service_email,
            service_password: service_password.to_string(),
        },
        crud,
        tenant_id,
        user_id,
    }
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn one_graphql_query_aggregates_real_data_from_two_independent_services() {
    let pool = connect_db().await;

    let jira_like = spin_up_harness(pool.clone(), "jira", simple_entity("test.gw_projects", "Project")).await;
    let crm_like = spin_up_harness(pool.clone(), "crm", simple_entity("test.gw_customers", "Customer")).await;

    // Seed one real record directly on each harness's own `CrudService` — this is the data the
    // aggregated query below must actually find, proving the gateway reached the real upstream,
    // not a stub.
    let jira_ctx = admin_context(jira_like.tenant_id, jira_like.user_id);
    jira_like
        .crud
        .create(
            "test.gw_projects",
            &serde_json::json!({ "name": "Gateway Demo Project" })
                .as_object()
                .unwrap()
                .clone(),
            &jira_ctx,
        )
        .await
        .unwrap();
    let crm_ctx = admin_context(crm_like.tenant_id, crm_like.user_id);
    crm_like
        .crud
        .create(
            "test.gw_customers",
            &serde_json::json!({ "name": "Gateway Demo Customer" })
                .as_object()
                .unwrap()
                .clone(),
            &crm_ctx,
        )
        .await
        .unwrap();

    // This is the actual boot-sequence call the real `graphql-gateway` binary makes — discovers
    // both upstreams' schemas over real HTTP and connects a real `GrpcBackend` to each.
    let built = schema_builder::build(&[jira_like.upstream, crm_like.upstream])
        .await
        .unwrap();
    assert_eq!(
        built.entity_count, 2,
        "both upstreams' entities must be registered into one schema"
    );

    // The proof: ONE GraphQL query, two root fields belonging to two different entities owned by
    // two different, independently-running services — a single response containing both.
    let query = r#"
        {
            testGwProjectsList { records { name } }
            testGwCustomersList { records { name } }
        }
    "#;
    let request = metap_graphql::with_request_data(
        async_graphql::Request::new(query),
        built.backend.clone(),
        // No `forwarded_bearer_token` here (this test calls `schema.execute` directly, bypassing
        // `server.rs::authenticate`, so there's no real inbound token to carry) — `GrpcBackend`
        // falls back to its configured `service_jwt` in that case, same as before this test's own
        // scope needs to verify (see `pick_token`'s unit tests, `metap-grpc/src/client.rs`, for
        // the forwarded-token-present case instead).
        RequestContext {
            tenant_id: Uuid::new_v4().to_string(),
            user_id: None,
            roles: None,
            function_id: None,
            context_attributes: None,
            forwarded_bearer_token: None,
        },
    );
    let response = built.schema.execute(request).await;
    assert!(
        response.errors.is_empty(),
        "unexpected GraphQL errors: {:?}",
        response.errors
    );

    let data = response.data.into_json().unwrap();
    let project_names: Vec<_> = data["testGwProjectsList"]["records"]
        .as_array()
        .unwrap()
        .iter()
        .map(|r| r["name"].clone())
        .collect();
    assert!(
        project_names.contains(&serde_json::json!("Gateway Demo Project")),
        "expected the jira-harness record in the aggregated response, got: {project_names:?}"
    );

    let customer_names: Vec<_> = data["testGwCustomersList"]["records"]
        .as_array()
        .unwrap()
        .iter()
        .map(|r| r["name"].clone())
        .collect();
    assert!(
        customer_names.contains(&serde_json::json!("Gateway Demo Customer")),
        "expected the crm-harness record in the aggregated response, got: {customer_names:?}"
    );

    sqlx::query("DELETE FROM records WHERE tenant_id IN ($1, $2)")
        .bind(jira_like.tenant_id)
        .bind(crm_like.tenant_id)
        .execute(&pool)
        .await
        .ok();
}
