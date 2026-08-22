//! E2E test: a real axum server, bound to a real socket, hit with real HTTP requests over
//! a real network stack — auth included (a real RS256 JWT, minted and verified, not
//! stubbed). `#[ignore]`d — see `metap-query/tests/query_planner_postgres.rs`'s doc comment
//! for the convention (unit tests never touch a DB; this needs both a DB and a live
//! server, run explicitly via `cargo test -- --ignored`).

use std::process::Command;
use std::sync::Arc;

use arc_swap::ArcSwap;
use axum::Router;
use jsonwebtoken::DecodingKey;
use metap_http::{build_router, AppState};
use metap_metadata::{
    EntityDefinition, EntityField, EntityListView, EntityWorkflow, FieldKind, MetadataRegistry, WorkflowTransition,
};
use metap_permission::PermissionService;
use serde_json::json;
use sqlx::postgres::PgPoolOptions;
use sqlx::PgPool;
use uuid::Uuid;

/// `docs/roadmap.md` Phase 16 gap, closed 2026-08-20: `AppState::new` now takes a `Router`
/// (shared with `PostgresPolicyStore`) instead of building one internally from a
/// `SecretStore`. No `control.tenants` row is ever inserted by these tests, so
/// `Router::begin` always takes the unregistered-tenant fallback (public schema) — same
/// behavior every route here had before this refactor.
fn test_router(pool: PgPool) -> metap_control::Router {
    let registry = Arc::new(metap_control::PostgresTenantRegistry::new(pool.clone()));
    metap_control::Router::new(
        pool,
        metap_control::RegistryCache::new(registry),
        Arc::new(metap_control::EnvStore),
    )
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

/// Delegates to `metap_peripherals::mint_jwt` — the same function `POST /auth/login` and
/// `dev-tools mint-token` use — instead of hand-rolling a `Claims` struct here, so this test
/// can't drift from the real claim shape (`iss`/`aud` in particular: a hand-rolled struct
/// without them would silently start failing once the verify side started checking them).
fn mint_token(private_pem: &str, tenant_id: Uuid, user_id: Uuid) -> String {
    metap_peripherals::mint_jwt(private_pem, tenant_id, user_id, 3600).unwrap()
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
    let database_url = std::env::var("DATABASE_URL").expect("DATABASE_URL required for this e2e test");
    PgPoolOptions::new()
        .max_connections(5)
        .connect(&database_url)
        .await
        .unwrap()
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
    let registry = Arc::new(registry);
    let permissions = PermissionService::new(Box::new(metap_control::PostgresPolicyStore::new(test_router(
        pool.clone(),
    ))));
    let decoding_key = DecodingKey::from_rsa_pem(public_pem.as_bytes()).unwrap();
    let state = AppState::new(
        pool.clone(),
        registry.clone(),
        Arc::new(ArcSwap::new(registry)),
        Arc::new(permissions),
        decoding_key,
        private_pem.clone(),
        test_router(pool.clone()),
    );
    // A real origin list, not empty — exercises the `allow_credentials` +
    // explicit-origin/header CORS branch (see `lib.rs`'s doc comment on the panic this
    // once triggered; an empty list here would silently skip that branch again).
    let router = build_router(state, &["http://localhost:5173".to_string()], Router::new());

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
    assert_eq!(
        with_trace_id.headers().get("x-trace-id").unwrap(),
        "caller-supplied-trace-id"
    );

    // openapi.json is public
    let openapi = client
        .get(format!("{base}/metadata/openapi.json"))
        .send()
        .await
        .unwrap();
    assert_eq!(openapi.status(), 200);

    // records route without a token -> 401, with requestId/traceId injected into the error body
    let unauthed = client.get(format!("{base}/api/test.orders")).send().await.unwrap();
    assert_eq!(unauthed.status(), 401);
    let expected_request_id = unauthed
        .headers()
        .get("x-request-id")
        .unwrap()
        .to_str()
        .unwrap()
        .to_string();
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

    sqlx::query("DELETE FROM outbox_events WHERE aggregate_type = 'test.orders'")
        .execute(&pool)
        .await
        .ok();
    sqlx::query("DELETE FROM workflow_events WHERE tenant_id = $1")
        .bind(tenant_id)
        .execute(&pool)
        .await
        .ok();
    sqlx::query("DELETE FROM records WHERE tenant_id = $1")
        .bind(tenant_id)
        .execute(&pool)
        .await
        .ok();
    sqlx::query("DELETE FROM user_roles WHERE tenant_id = $1")
        .bind(tenant_id)
        .execute(&pool)
        .await
        .ok();
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn rate_limit_returns_429_once_the_burst_is_exhausted() {
    let pool = connect().await;

    let mut registry = MetadataRegistry::new();
    registry.register(test_entity()).unwrap();
    let registry = Arc::new(registry);
    let permissions = PermissionService::new(Box::new(metap_control::PostgresPolicyStore::new(test_router(
        pool.clone(),
    ))));
    let keydir = tempdir();
    let (private_pem, public_pem) = openssl_genrsa(keydir.path());
    let decoding_key = DecodingKey::from_rsa_pem(public_pem.as_bytes()).unwrap();
    let state = AppState::new(
        pool.clone(),
        registry.clone(),
        Arc::new(ArcSwap::new(registry)),
        Arc::new(permissions),
        decoding_key,
        private_pem,
        test_router(pool.clone()),
    );
    let router = build_router(state, &[], Router::new());

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
    let (has_retry_after, body) =
        rate_limited.expect("expected at least one 429 among 400 concurrent requests against a 300-request burst");
    assert!(has_retry_after);
    assert_eq!(body["error"]["code"], "too_many_requests");
    assert!(body["error"]["requestId"].is_string());
    assert!(body["error"]["traceId"].is_string());
}

/// `PlatformAdminContext` (Phase 16 Giai đoạn 3, `docs/roadmap.md`) — no real business route
/// uses it (that lives in the separate, untested-by-design `metap-control-http` crate, same
/// convention as `metap-lowcode-http`), so this mounts a throwaway test-only route via
/// `build_router`'s `extra_routes` argument just to exercise the extractor's gating logic in
/// isolation: a normal tenant admin (even with the `"admin"` role) must NOT pass, a user in
/// the platform sentinel tenant without `"platform_admin"` must NOT pass, and only a user in
/// the platform sentinel tenant *with* `"platform_admin"` passes.
#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn platform_admin_context_gates_by_sentinel_tenant_and_role() {
    let pool = connect().await;

    async fn platform_only_handler(
        metap_http::PlatformAdminContext(_context): metap_http::PlatformAdminContext,
    ) -> &'static str {
        "ok"
    }
    let extra_routes = Router::new().route("/test/platform-only", axum::routing::get(platform_only_handler));

    let keydir = tempdir();
    let (private_pem, public_pem) = openssl_genrsa(keydir.path());

    let registry = Arc::new(MetadataRegistry::new());
    let permissions = PermissionService::new(Box::new(metap_control::PostgresPolicyStore::new(test_router(
        pool.clone(),
    ))));
    let decoding_key = DecodingKey::from_rsa_pem(public_pem.as_bytes()).unwrap();
    let state = AppState::new(
        pool.clone(),
        registry.clone(),
        Arc::new(ArcSwap::new(registry)),
        Arc::new(permissions),
        decoding_key,
        private_pem.clone(),
        test_router(pool.clone()),
    );
    let router = build_router(state, &[], extra_routes);

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

    // a normal tenant's admin — even with the "admin" role — is not a platform admin
    let ordinary_tenant_id = Uuid::new_v4();
    let ordinary_user_id = Uuid::new_v4();
    sqlx::query("INSERT INTO user_roles (tenant_id, user_id, role) VALUES ($1, $2, 'admin')")
        .bind(ordinary_tenant_id)
        .bind(ordinary_user_id)
        .execute(&pool)
        .await
        .unwrap();
    let ordinary_token = mint_token(&private_pem, ordinary_tenant_id, ordinary_user_id);
    let res = client
        .get(format!("{base}/test/platform-only"))
        .bearer_auth(&ordinary_token)
        .send()
        .await
        .unwrap();
    assert_eq!(res.status(), 403);

    // the platform sentinel tenant without the "platform_admin" role still doesn't pass
    let unprivileged_platform_user_id = Uuid::new_v4();
    let unprivileged_token = mint_token(
        &private_pem,
        metap_control::PLATFORM_TENANT_ID,
        unprivileged_platform_user_id,
    );
    let res = client
        .get(format!("{base}/test/platform-only"))
        .bearer_auth(&unprivileged_token)
        .send()
        .await
        .unwrap();
    assert_eq!(res.status(), 403);

    // platform sentinel tenant + "platform_admin" role passes
    let platform_admin_user_id = Uuid::new_v4();
    sqlx::query("INSERT INTO user_roles (tenant_id, user_id, role) VALUES ($1, $2, 'platform_admin')")
        .bind(metap_control::PLATFORM_TENANT_ID)
        .bind(platform_admin_user_id)
        .execute(&pool)
        .await
        .unwrap();
    let platform_admin_token = mint_token(&private_pem, metap_control::PLATFORM_TENANT_ID, platform_admin_user_id);
    let res = client
        .get(format!("{base}/test/platform-only"))
        .bearer_auth(&platform_admin_token)
        .send()
        .await
        .unwrap();
    assert_eq!(res.status(), 200);

    sqlx::query("DELETE FROM user_roles WHERE tenant_id = $1")
        .bind(ordinary_tenant_id)
        .execute(&pool)
        .await
        .ok();
    sqlx::query("DELETE FROM user_roles WHERE tenant_id = $1 AND user_id = $2")
        .bind(metap_control::PLATFORM_TENANT_ID)
        .bind(platform_admin_user_id)
        .execute(&pool)
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
    }
}

fn plain_string_entity(name: &str, field_names: &[&str]) -> EntityDefinition {
    EntityDefinition {
        name: name.to_string(),
        label: name.to_string(),
        table_name: "records".to_string(),
        fields: field_names.iter().map(|f| string_field(f)).collect(),
        list_views: vec![],
        workflow: None,
    }
}

/// Live verification of A4/A4b (`docs/features/03-organization-identity.md`): `AUTH_CONTEXT_ENTITY`
/// enriches `RequestContext` from the caller's own record on a configured entity (`test.profiles`
/// here, standing in for `hr.employees`), an org-scoped ABAC policy on `test.tasks` reads that
/// enrichment via `fromContext`, and the enrichment is cached — a stale cached value persists
/// until `POST /admin/users/{userId}/context/invalidate` clears it explicitly (a long TTL is used
/// so this test doesn't race a timer).
#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn auth_context_entity_enriches_org_scoped_policies_and_supports_explicit_cache_invalidation() {
    let pool = connect().await;
    let tenant_id = Uuid::new_v4();
    let admin_user_id = Uuid::new_v4();
    let employee_user_id = Uuid::new_v4();

    sqlx::query("INSERT INTO user_roles (tenant_id, user_id, role) VALUES ($1, $2, 'admin')")
        .bind(tenant_id)
        .bind(admin_user_id)
        .execute(&pool)
        .await
        .unwrap();
    sqlx::query("INSERT INTO user_roles (tenant_id, user_id, role) VALUES ($1, $2, 'employee')")
        .bind(tenant_id)
        .bind(employee_user_id)
        .execute(&pool)
        .await
        .unwrap();

    let keydir = tempdir();
    let (private_pem, public_pem) = openssl_genrsa(keydir.path());
    let admin_token = mint_token(&private_pem, tenant_id, admin_user_id);
    let employee_token = mint_token(&private_pem, tenant_id, employee_user_id);

    let mut registry = MetadataRegistry::new();
    registry
        .register(plain_string_entity("test.profiles", &["userId", "deptId"]))
        .unwrap();
    registry
        .register(plain_string_entity("test.tasks", &["deptId", "title"]))
        .unwrap();
    let registry = Arc::new(registry);
    let permissions = PermissionService::new(Box::new(metap_control::PostgresPolicyStore::new(test_router(
        pool.clone(),
    ))));
    let decoding_key = DecodingKey::from_rsa_pem(public_pem.as_bytes()).unwrap();
    let mut state = AppState::new(
        pool.clone(),
        registry.clone(),
        Arc::new(ArcSwap::new(registry)),
        Arc::new(permissions),
        decoding_key,
        private_pem.clone(),
        test_router(pool.clone()),
    );
    state.auth_context_entity = Some(Arc::from("test.profiles"));
    state.context_attributes_cache =
        metap_http::cache::ContextAttributesCache::new(std::time::Duration::from_secs(3600));
    let router = build_router(state, &[], Router::new());

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

    // employee's own "profile" record — this is what AUTH_CONTEXT_ENTITY reads.
    sqlx::query("INSERT INTO records (tenant_id, entity, data, version) VALUES ($1, 'test.profiles', $2, 1)")
        .bind(tenant_id)
        .bind(json!({ "userId": employee_user_id.to_string(), "deptId": "eng" }))
        .execute(&pool)
        .await
        .unwrap();

    // admin bypasses policy checks entirely -> seed two task records in different departments
    let eng_task: serde_json::Value = client
        .post(format!("{base}/api/test.tasks"))
        .bearer_auth(&admin_token)
        .json(&json!({ "data": { "deptId": "eng", "title": "Eng task" } }))
        .send()
        .await
        .unwrap()
        .json()
        .await
        .unwrap();
    let eng_task_id = eng_task["data"]["id"].as_str().unwrap().to_string();

    let sales_task: serde_json::Value = client
        .post(format!("{base}/api/test.tasks"))
        .bearer_auth(&admin_token)
        .json(&json!({ "data": { "deptId": "sales", "title": "Sales task" } }))
        .send()
        .await
        .unwrap()
        .json()
        .await
        .unwrap();
    let sales_task_id = sales_task["data"]["id"].as_str().unwrap().to_string();

    // grant "employee" bare read access (context-subject, RBAC only) ...
    let policy1 = client
        .post(format!("{base}/admin/policies"))
        .bearer_auth(&admin_token)
        .json(&json!({ "entity": "test.tasks", "action": "read", "roles": ["employee"], "subject": "context" }))
        .send()
        .await
        .unwrap();
    assert_eq!(policy1.status(), 201);
    // ... then narrow it to same-department records only (record-subject, ABAC condition reading
    // the enrichment this whole feature adds).
    let policy2 = client
        .post(format!("{base}/admin/policies"))
        .bearer_auth(&admin_token)
        .json(&json!({
            "entity": "test.tasks",
            "action": "read",
            "subject": "record",
            "condition": { "attribute": "deptId", "op": "eq", "value": { "fromContext": "deptId" } }
        }))
        .send()
        .await
        .unwrap();
    assert_eq!(policy2.status(), 201);

    // employee (deptId=eng, from their test.profiles record) reads the eng task ...
    let res = client
        .get(format!("{base}/api/test.tasks/{eng_task_id}"))
        .bearer_auth(&employee_token)
        .send()
        .await
        .unwrap();
    assert_eq!(res.status(), 200);
    // ... but not the sales task — deny-by-default, no matching record-level policy.
    let res = client
        .get(format!("{base}/api/test.tasks/{sales_task_id}"))
        .bearer_auth(&employee_token)
        .send()
        .await
        .unwrap();
    assert_eq!(res.status(), 403);

    // move the employee to "sales" ...
    sqlx::query(
        "UPDATE records SET data = jsonb_set(data, '{deptId}', '\"sales\"') \
         WHERE tenant_id = $1 AND entity = 'test.profiles' AND data ->> 'userId' = $2",
    )
    .bind(tenant_id)
    .bind(employee_user_id.to_string())
    .execute(&pool)
    .await
    .unwrap();

    // ... the cache still holds the stale "eng" attribute (long TTL, no invalidation yet) — the
    // employee's *next* request still resolves against the old department.
    let res = client
        .get(format!("{base}/api/test.tasks/{eng_task_id}"))
        .bearer_auth(&employee_token)
        .send()
        .await
        .unwrap();
    assert_eq!(
        res.status(),
        200,
        "cached context_attributes should still be stale (deptId=eng)"
    );

    // explicit invalidate clears it immediately, without waiting on the TTL.
    let invalidate_res = client
        .post(format!("{base}/admin/users/{employee_user_id}/context/invalidate"))
        .bearer_auth(&admin_token)
        .send()
        .await
        .unwrap();
    assert_eq!(invalidate_res.status(), 204);

    // now the employee's context is fresh: eng is no longer reachable, sales is.
    let res = client
        .get(format!("{base}/api/test.tasks/{eng_task_id}"))
        .bearer_auth(&employee_token)
        .send()
        .await
        .unwrap();
    assert_eq!(
        res.status(),
        403,
        "post-invalidate context should be fresh (deptId=sales)"
    );
    let res = client
        .get(format!("{base}/api/test.tasks/{sales_task_id}"))
        .bearer_auth(&employee_token)
        .send()
        .await
        .unwrap();
    assert_eq!(
        res.status(),
        200,
        "post-invalidate context should now see the sales task"
    );

    sqlx::query("DELETE FROM outbox_events WHERE aggregate_type = 'test.tasks'")
        .execute(&pool)
        .await
        .ok();
    sqlx::query("DELETE FROM policies WHERE tenant_id = $1")
        .bind(tenant_id)
        .execute(&pool)
        .await
        .ok();
    sqlx::query("DELETE FROM records WHERE tenant_id = $1")
        .bind(tenant_id)
        .execute(&pool)
        .await
        .ok();
    sqlx::query("DELETE FROM user_roles WHERE tenant_id = $1")
        .bind(tenant_id)
        .execute(&pool)
        .await
        .ok();
}

/// Regression test for a real gap found in code review (2026-08-22): the *only* invalidation
/// path exercised above is an admin remembering to call
/// `POST /admin/users/{userId}/context/invalidate` after editing the underlying record — an
/// easy step to forget. Editing the same `AUTH_CONTEXT_ENTITY` record through the ordinary
/// `PATCH /api/:entity/:id` path must invalidate the cache automatically, no separate call
/// needed. Reuses the same `test.profiles`/`test.tasks`/org-scoped-policy shape as the test
/// above, but changes `deptId` via a real `PATCH` instead of raw SQL, and never calls the
/// explicit invalidate endpoint at all.
#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn updating_the_auth_context_entity_record_via_patch_invalidates_the_cache_automatically() {
    let pool = connect().await;
    let tenant_id = Uuid::new_v4();
    let admin_user_id = Uuid::new_v4();
    let employee_user_id = Uuid::new_v4();

    sqlx::query("INSERT INTO user_roles (tenant_id, user_id, role) VALUES ($1, $2, 'admin')")
        .bind(tenant_id)
        .bind(admin_user_id)
        .execute(&pool)
        .await
        .unwrap();
    sqlx::query("INSERT INTO user_roles (tenant_id, user_id, role) VALUES ($1, $2, 'employee')")
        .bind(tenant_id)
        .bind(employee_user_id)
        .execute(&pool)
        .await
        .unwrap();

    let keydir = tempdir();
    let (private_pem, public_pem) = openssl_genrsa(keydir.path());
    let admin_token = mint_token(&private_pem, tenant_id, admin_user_id);
    let employee_token = mint_token(&private_pem, tenant_id, employee_user_id);

    let mut registry = MetadataRegistry::new();
    registry
        .register(plain_string_entity("test.profiles", &["userId", "deptId"]))
        .unwrap();
    registry
        .register(plain_string_entity("test.tasks", &["deptId", "title"]))
        .unwrap();
    let registry = Arc::new(registry);
    let permissions = PermissionService::new(Box::new(metap_control::PostgresPolicyStore::new(test_router(
        pool.clone(),
    ))));
    let decoding_key = DecodingKey::from_rsa_pem(public_pem.as_bytes()).unwrap();
    let mut state = AppState::new(
        pool.clone(),
        registry.clone(),
        Arc::new(ArcSwap::new(registry)),
        Arc::new(permissions),
        decoding_key,
        private_pem.clone(),
        test_router(pool.clone()),
    );
    state.auth_context_entity = Some(Arc::from("test.profiles"));
    // Long TTL, same reasoning as the test above: proves invalidation is automatic, not that
    // it happened to occur right as a short TTL also expired.
    state.context_attributes_cache =
        metap_http::cache::ContextAttributesCache::new(std::time::Duration::from_secs(3600));
    let router = build_router(state, &[], Router::new());

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

    // employee's own "profile" record, created through the API (not raw SQL) so its id/version
    // are on hand for the PATCH below.
    let profile: serde_json::Value = client
        .post(format!("{base}/api/test.profiles"))
        .bearer_auth(&admin_token)
        .json(&json!({ "data": { "userId": employee_user_id.to_string(), "deptId": "eng" } }))
        .send()
        .await
        .unwrap()
        .json()
        .await
        .unwrap();
    let profile_id = profile["data"]["id"].as_str().unwrap().to_string();
    let profile_version = profile["data"]["version"].as_i64().unwrap();

    let eng_task: serde_json::Value = client
        .post(format!("{base}/api/test.tasks"))
        .bearer_auth(&admin_token)
        .json(&json!({ "data": { "deptId": "eng", "title": "Eng task" } }))
        .send()
        .await
        .unwrap()
        .json()
        .await
        .unwrap();
    let eng_task_id = eng_task["data"]["id"].as_str().unwrap().to_string();

    let sales_task: serde_json::Value = client
        .post(format!("{base}/api/test.tasks"))
        .bearer_auth(&admin_token)
        .json(&json!({ "data": { "deptId": "sales", "title": "Sales task" } }))
        .send()
        .await
        .unwrap()
        .json()
        .await
        .unwrap();
    let sales_task_id = sales_task["data"]["id"].as_str().unwrap().to_string();

    client
        .post(format!("{base}/admin/policies"))
        .bearer_auth(&admin_token)
        .json(&json!({ "entity": "test.tasks", "action": "read", "roles": ["employee"], "subject": "context" }))
        .send()
        .await
        .unwrap();
    client
        .post(format!("{base}/admin/policies"))
        .bearer_auth(&admin_token)
        .json(&json!({
            "entity": "test.tasks",
            "action": "read",
            "subject": "record",
            "condition": { "attribute": "deptId", "op": "eq", "value": { "fromContext": "deptId" } }
        }))
        .send()
        .await
        .unwrap();

    // employee (deptId=eng) can reach the eng task, primes the cache.
    let res = client
        .get(format!("{base}/api/test.tasks/{eng_task_id}"))
        .bearer_auth(&employee_token)
        .send()
        .await
        .unwrap();
    assert_eq!(res.status(), 200);

    // an ordinary PATCH to the profile record — not raw SQL, not the explicit invalidate
    // endpoint — moves the employee to "sales".
    let patch_res = client
        .patch(format!("{base}/api/test.profiles/{profile_id}"))
        .bearer_auth(&admin_token)
        .json(&json!({ "version": profile_version, "data": { "deptId": "sales" } }))
        .send()
        .await
        .unwrap();
    assert_eq!(patch_res.status(), 200);

    // the employee's *very next* request already sees the new department — no explicit
    // invalidate call, no waiting on the TTL.
    let res = client
        .get(format!("{base}/api/test.tasks/{eng_task_id}"))
        .bearer_auth(&employee_token)
        .send()
        .await
        .unwrap();
    assert_eq!(
        res.status(),
        403,
        "PATCH to the profile record should have auto-invalidated the cache (deptId=sales now)"
    );
    let res = client
        .get(format!("{base}/api/test.tasks/{sales_task_id}"))
        .bearer_auth(&employee_token)
        .send()
        .await
        .unwrap();
    assert_eq!(res.status(), 200, "employee should now reach the sales task instead");

    sqlx::query("DELETE FROM outbox_events WHERE aggregate_type IN ('test.tasks', 'test.profiles')")
        .execute(&pool)
        .await
        .ok();
    sqlx::query("DELETE FROM policies WHERE tenant_id = $1")
        .bind(tenant_id)
        .execute(&pool)
        .await
        .ok();
    sqlx::query("DELETE FROM records WHERE tenant_id = $1")
        .bind(tenant_id)
        .execute(&pool)
        .await
        .ok();
    sqlx::query("DELETE FROM user_roles WHERE tenant_id = $1")
        .bind(tenant_id)
        .execute(&pool)
        .await
        .ok();
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
