//! A real axum server, bound to a real socket, hit with real HTTP requests — auth included
//! (a real RS256 JWT, minted and verified, not stubbed). `#[ignore]`d: needs `DATABASE_URL`/
//! `RABBITMQ_URL` pointed at a real Postgres/RabbitMQ, run explicitly via
//! `cargo test -- --ignored`. Mirrors the pattern (and the reason for it — integration
//! tests only see a crate's *library* target, and this project only has a binary, so the
//! test entity is defined here rather than imported from `src/example_entity.rs`) used by
//! `metap-http`'s own e2e test in the metap repo.

use std::process::Command;
use std::sync::Arc;

use arc_swap::ArcSwap;
use axum::Router;
use jsonwebtoken::{encode, EncodingKey, Header};
use metap::prelude::*;
use serde::Serialize;
use serde_json::json;
use sqlx::postgres::PgPoolOptions;
use sqlx::PgPool;
use uuid::Uuid;

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

    (std::fs::read_to_string(private_path).unwrap(), std::fs::read_to_string(public_path).unwrap())
}

#[derive(Serialize)]
struct Claims {
    sub: String,
    #[serde(rename = "tenantId")]
    tenant_id: String,
    exp: usize,
}

fn mint_token(private_pem: &str, tenant_id: Uuid, user_id: Uuid) -> String {
    let claims = Claims {
        sub: user_id.to_string(),
        tenant_id: tenant_id.to_string(),
        exp: (chrono::Utc::now().timestamp() + 3600) as usize,
    };
    let key = EncodingKey::from_rsa_pem(private_pem.as_bytes()).unwrap();
    encode(&Header::new(jsonwebtoken::Algorithm::RS256), &claims, &key).unwrap()
}

fn test_entity() -> EntityDefinition {
    EntityDefinition {
        name: "test.tasks".to_string(),
        label: "Task".to_string(),
        table_name: "records".to_string(),
        fields: vec![EntityField {
            name: "title".to_string(),
            label: "Title".to_string(),
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
        }],
        list_views: vec![EntityListView {
            name: "default".to_string(),
            label: "Default".to_string(),
            fields: vec!["title".to_string()],
            filters: vec![],
            default_sort: Some("-createdAt".to_string()),
            max_limit: 50,
        }],
        workflow: None,
    }
}

async fn connect() -> PgPool {
    let database_url = std::env::var("DATABASE_URL").expect("DATABASE_URL required for this e2e test");
    PgPoolOptions::new().max_connections(5).connect(&database_url).await.unwrap()
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running Postgres"]
async fn full_http_lifecycle_over_a_real_server_and_a_real_jwt() {
    let pool = connect().await;
    let tenant_id = Uuid::new_v4();
    let user_id = Uuid::new_v4();

    let keydir = std::env::temp_dir().join(format!("{{project-name}}-test-{}", Uuid::new_v4()));
    std::fs::create_dir_all(&keydir).unwrap();
    let (private_pem, public_pem) = openssl_genrsa(&keydir);
    let token = mint_token(&private_pem, tenant_id, user_id);

    let mut registry = MetadataRegistry::new();
    registry.register(test_entity()).unwrap();
    let registry = Arc::new(registry);
    let tenant_registry = Arc::new(metap::control::PostgresTenantRegistry::new(pool.clone()));
    let test_router = metap::control::Router::new(
        pool.clone(),
        metap::control::RegistryCache::new(tenant_registry),
        Arc::new(metap::control::EnvStore),
    );
    let permissions = PermissionService::new(Box::new(PostgresPolicyStore::new(test_router.clone())));
    let decoding_key = jsonwebtoken::DecodingKey::from_rsa_pem(public_pem.as_bytes()).unwrap();
    let state = AppState::new(
        pool.clone(),
        registry.clone(),
        Arc::new(ArcSwap::new(registry)),
        Arc::new(permissions),
        decoding_key,
        private_pem.clone(),
        test_router,
    );
    let router = build_router(state, &[], Router::new());

    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = listener.local_addr().unwrap();
    tokio::spawn(async move {
        // The rate-limit layer inside `build_router` needs `ConnectInfo<SocketAddr>`.
        axum::serve(listener, router.into_make_service_with_connect_info::<std::net::SocketAddr>())
            .await
            .unwrap();
    });
    let base = format!("http://{addr}");
    let client = reqwest::Client::new();

    let health = client.get(format!("{base}/health")).send().await.unwrap();
    assert_eq!(health.status(), 200);

    let unauthed = client.get(format!("{base}/api/test.tasks")).send().await.unwrap();
    assert_eq!(unauthed.status(), 401);

    let create_res = client
        .post(format!("{base}/api/test.tasks"))
        .bearer_auth(&token)
        .json(&json!({ "data": { "title": "First" } }))
        .send()
        .await
        .unwrap();
    assert_eq!(create_res.status(), 201);
    let created: serde_json::Value = create_res.json().await.unwrap();
    let id = created["data"]["id"].as_str().unwrap().to_string();

    let get_res =
        client.get(format!("{base}/api/test.tasks/{id}")).bearer_auth(&token).send().await.unwrap();
    assert_eq!(get_res.status(), 200);

    sqlx::query("DELETE FROM records WHERE tenant_id = $1").bind(tenant_id).execute(&pool).await.ok();
    std::fs::remove_dir_all(&keydir).ok();
}
