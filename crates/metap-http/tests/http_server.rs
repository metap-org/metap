//! E2E test: a real axum server, bound to a real socket, hit with real HTTP requests over
//! a real network stack — auth included (a real RS256 JWT, minted and verified, not
//! stubbed). `#[ignore]`d — see `metap-query/tests/query_planner_postgres.rs`'s doc comment
//! for the convention (unit tests never touch a DB; this needs both a DB and a live
//! server, run explicitly via `cargo test -- --ignored`).

use std::process::Command;
use std::sync::Arc;

use jsonwebtoken::{encode, DecodingKey, EncodingKey, Header};
use metap_http::{build_router, AppState};
use metap_metadata::{
    EntityDefinition, EntityField, EntityListView, EntityWorkflow, FieldKind, MetadataRegistry,
    WorkflowTransition,
};
use metap_permission::PermissionService;
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

    (
        std::fs::read_to_string(private_path).unwrap(),
        std::fs::read_to_string(public_path).unwrap(),
    )
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
            }],
        }),
    }
}

async fn connect() -> PgPool {
    let database_url =
        std::env::var("DATABASE_URL").expect("DATABASE_URL required for this e2e test");
    PgPoolOptions::new().max_connections(5).connect(&database_url).await.unwrap()
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn full_http_lifecycle_over_a_real_server_and_a_real_jwt() {
    let pool = connect().await;
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
    let token = mint_token(&private_pem, tenant_id, user_id);

    let mut registry = MetadataRegistry::new();
    registry.register(test_entity()).unwrap();
    let permissions = PermissionService::new(Box::new(metap_permission::PostgresPolicyStore::new(pool.clone())));
    let decoding_key = DecodingKey::from_rsa_pem(public_pem.as_bytes()).unwrap();
    let state = AppState::new(pool.clone(), Arc::new(registry), Arc::new(permissions), decoding_key);
    // A real origin list, not empty — exercises the `allow_credentials` +
    // explicit-origin/header CORS branch (see `lib.rs`'s doc comment on the panic this
    // once triggered; an empty list here would silently skip that branch again).
    let router = build_router(state, &["http://localhost:5173".to_string()]);

    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = listener.local_addr().unwrap();
    tokio::spawn(async move {
        // Mirrors `apps/crm-server/src/main.rs` — the rate-limit layer needs
        // `ConnectInfo<SocketAddr>`, see `build_router`'s doc comment.
        axum::serve(
            listener,
            router.into_make_service_with_connect_info::<std::net::SocketAddr>(),
        )
        .await
        .unwrap();
    });
    let base = format!("http://{addr}");

    let client = reqwest::Client::new();

    // health is public, no auth needed
    let health = client.get(format!("{base}/health")).send().await.unwrap();
    assert_eq!(health.status(), 200);
    // helmet-equivalent security headers (see `security_headers.rs`) on every response
    assert_eq!(health.headers().get("x-content-type-options").unwrap(), "nosniff");
    assert_eq!(health.headers().get("x-frame-options").unwrap(), "SAMEORIGIN");
    assert!(health.headers().get("content-security-policy").is_some());
    // request-id/trace-id (see `request_context.rs`): a trace id is generated when none is
    // sent, and both ids are present as response headers
    assert!(health.headers().get("x-request-id").is_some());
    assert!(health.headers().get("x-trace-id").is_some());

    // an incoming x-trace-id is validated and echoed back rather than replaced
    let with_trace_id = client
        .get(format!("{base}/health"))
        .header("x-trace-id", "caller-supplied-trace-id")
        .send()
        .await
        .unwrap();
    assert_eq!(with_trace_id.headers().get("x-trace-id").unwrap(), "caller-supplied-trace-id");

    // openapi.json is public
    let openapi = client.get(format!("{base}/metadata/openapi.json")).send().await.unwrap();
    assert_eq!(openapi.status(), 200);

    // records route without a token -> 401, with requestId/traceId injected into the error body
    let unauthed = client.get(format!("{base}/api/test.orders")).send().await.unwrap();
    assert_eq!(unauthed.status(), 401);
    let expected_request_id = unauthed.headers().get("x-request-id").unwrap().to_str().unwrap().to_string();
    let unauthed_body: serde_json::Value = unauthed.json().await.unwrap();
    assert_eq!(unauthed_body["error"]["requestId"], expected_request_id);
    assert!(unauthed_body["error"]["traceId"].is_string());

    // create
    let create_res = client
        .post(format!("{base}/api/test.orders"))
        .bearer_auth(&token)
        .json(&json!({ "data": { "name": "First" } }))
        .send()
        .await
        .unwrap();
    assert_eq!(create_res.status(), 201);
    let created: serde_json::Value = create_res.json().await.unwrap();
    let id = created["data"]["id"].as_str().unwrap().to_string();
    assert_eq!(created["data"]["status"], "draft");
    let version = created["data"]["version"].as_i64().unwrap();

    // get
    let get_res = client
        .get(format!("{base}/api/test.orders/{id}"))
        .bearer_auth(&token)
        .send()
        .await
        .unwrap();
    assert_eq!(get_res.status(), 200);
    let fetched: serde_json::Value = get_res.json().await.unwrap();
    assert_eq!(fetched["data"]["id"], id);
    assert_eq!(fetched["data"]["capabilities"]["transitions"][0]["action"], "activate");

    // transition
    let transition_res = client
        .post(format!("{base}/api/test.orders/{id}/transitions/activate"))
        .bearer_auth(&token)
        .json(&json!({ "version": version }))
        .send()
        .await
        .unwrap();
    assert_eq!(transition_res.status(), 200);
    let transitioned: serde_json::Value = transition_res.json().await.unwrap();
    assert_eq!(transitioned["data"]["status"], "active");
    let version = transitioned["data"]["version"].as_i64().unwrap();

    // stale-version update -> 409 with the same error shape the TS error-handler produces
    let conflict_res = client
        .patch(format!("{base}/api/test.orders/{id}"))
        .bearer_auth(&token)
        .json(&json!({ "version": 999, "data": { "name": "Changed" } }))
        .send()
        .await
        .unwrap();
    assert_eq!(conflict_res.status(), 409);
    let conflict_body: serde_json::Value = conflict_res.json().await.unwrap();
    assert_eq!(conflict_body["error"]["code"], "version_conflict");

    // delete
    let delete_res = client
        .delete(format!("{base}/api/test.orders/{id}"))
        .bearer_auth(&token)
        .json(&json!({ "version": version }))
        .send()
        .await
        .unwrap();
    assert_eq!(delete_res.status(), 200);

    // post-delete get -> 404
    let after_delete = client
        .get(format!("{base}/api/test.orders/{id}"))
        .bearer_auth(&token)
        .send()
        .await
        .unwrap();
    assert_eq!(after_delete.status(), 404);

    sqlx::query("DELETE FROM outbox_events WHERE aggregate_type = 'test.orders'").execute(&pool).await.ok();
    sqlx::query("DELETE FROM workflow_events WHERE tenant_id = $1").bind(tenant_id).execute(&pool).await.ok();
    sqlx::query("DELETE FROM records WHERE tenant_id = $1").bind(tenant_id).execute(&pool).await.ok();
    sqlx::query("DELETE FROM user_roles WHERE tenant_id = $1").bind(tenant_id).execute(&pool).await.ok();
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn rate_limit_returns_429_once_the_burst_is_exhausted() {
    let pool = connect().await;

    let mut registry = MetadataRegistry::new();
    registry.register(test_entity()).unwrap();
    let permissions = PermissionService::new(Box::new(metap_permission::PostgresPolicyStore::new(pool.clone())));
    let keydir = tempdir();
    let (_private_pem, public_pem) = openssl_genrsa(keydir.path());
    let decoding_key = DecodingKey::from_rsa_pem(public_pem.as_bytes()).unwrap();
    let state = AppState::new(pool, Arc::new(registry), Arc::new(permissions), decoding_key);
    let router = build_router(state, &[]);

    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = listener.local_addr().unwrap();
    tokio::spawn(async move {
        axum::serve(
            listener,
            router.into_make_service_with_connect_info::<std::net::SocketAddr>(),
        )
        .await
        .unwrap();
    });
    let base = format!("http://{addr}");
    let client = reqwest::Client::new();

    // Burst capacity is 300 (see `build_router`'s doc comment) — a fresh
    // `GovernorConfig`/limiter per `build_router` call, so this is isolated from the
    // lifecycle test above even though both hit a real server on loopback. Fired
    // concurrently, not sequentially: at 5 tokens/sec replenishment, 305 sequential
    // round-trips over loopback take long enough for the bucket to partially refill and
    // never actually trip the limit.
    let mut in_flight = tokio::task::JoinSet::new();
    for _ in 0..400 {
        let client = client.clone();
        let base = base.clone();
        in_flight.spawn(async move {
            let res = client.get(format!("{base}/health")).send().await.unwrap();
            if res.status() == reqwest::StatusCode::TOO_MANY_REQUESTS {
                let has_retry_after = res.headers().get("retry-after").is_some();
                let body: serde_json::Value = res.json().await.unwrap();
                Some((has_retry_after, body))
            } else {
                None
            }
        });
    }
    let mut rate_limited = None;
    while let Some(result) = in_flight.join_next().await {
        if let Some(hit) = result.unwrap() {
            rate_limited = Some(hit);
        }
    }
    let (has_retry_after, body) = rate_limited
        .expect("expected at least one 429 among 400 concurrent requests against a 300-request burst");
    assert!(has_retry_after);
    assert_eq!(body["error"]["code"], "too_many_requests");
    assert!(body["error"]["requestId"].is_string());
    assert!(body["error"]["traceId"].is_string());
}

fn tempdir() -> TempDir {
    TempDir::new()
}

struct TempDir(std::path::PathBuf);

impl TempDir {
    fn new() -> Self {
        let path = std::env::temp_dir().join(format!("metap-http-test-{}", Uuid::new_v4()));
        std::fs::create_dir_all(&path).unwrap();
        Self(path)
    }

    fn path(&self) -> &std::path::Path {
        &self.0
    }
}

impl Drop for TempDir {
    fn drop(&mut self) {
        std::fs::remove_dir_all(&self.0).ok();
    }
}
