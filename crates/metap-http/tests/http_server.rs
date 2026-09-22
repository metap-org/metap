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

/// A dedicated table this file creates itself (`full_http_lifecycle_over_a_real_server_and_a_real_jwt`,
/// `CREATE TABLE IF NOT EXISTS`) — the shared `records` table this used to point at no longer
/// exists at all (`crates/migrations/0033_drop_records_table.sql`).
const TEST_ORDERS_TABLE: &str = "entities.test_orders";

fn test_entity() -> EntityDefinition {
    EntityDefinition {
        name: "test.orders".to_string(),
        label: "Order".to_string(),
        table_name: TEST_ORDERS_TABLE.to_string(),
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
                computed: None,
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
                computed: None,
            },
        ],
        list_views: vec![EntityListView {
            name: "default".to_string(),
            label: "Default".to_string(),
            fields: vec!["name".to_string()],
            filters: vec![],
            required_fields: vec![],
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
        unique_constraints: vec![],
        audit: None,
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

/// **Moved off REST 2026-09-21** — entity CRUD is GraphQL-only now (no more REST
/// `routes::records`, see `metap-http`'s own `CLAUDE.md` bullet), so the create/get/transition/
/// conflict/delete portion of this test goes through `/graphql` (mounted as `extra_routes`,
/// exactly as a downstream binary mounts it) instead of `/api/test.orders*`. The transport-level
/// assertions this test exists to catch regressions in — security headers, request/trace id,
/// the CORS `allow_credentials` branch, `/metadata/openapi.json` staying public — are unchanged
/// and still real: none of them are specific to which route family sits behind them.
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

    sqlx::query("CREATE SCHEMA IF NOT EXISTS entities")
        .execute(&pool)
        .await
        .unwrap();
    sqlx::query(&format!(
        "CREATE TABLE IF NOT EXISTS {TEST_ORDERS_TABLE} (
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
    let graphql_routes = metap_graphql_http::router(&state, metap_graphql::SchemaLimits::default()).unwrap();
    // A real origin list, not empty — exercises the `allow_credentials` +
    // explicit-origin/header CORS branch (see `lib.rs`'s doc comment on the panic this
    // once triggered; an empty list here would silently skip that branch again).
    let router = build_router(state, &["http://localhost:5173".to_string()], graphql_routes);

    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = listener.local_addr().unwrap();
    tokio::spawn(async move {
        // Mirrors `../metap-demo-crm/src/main.rs` — the rate-limit layer needs
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

    // /graphql without a token -> 401, with requestId/traceId injected into the error body — the
    // same `AuthContext` extractor REST used, gating the whole endpoint before any resolver runs.
    let unauthed = client
        .post(format!("{base}/graphql"))
        .json(&json!({ "query": "{ testOrdersList { records { id } } }" }))
        .send()
        .await
        .unwrap();
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
    let create_res: serde_json::Value = client
        .post(format!("{base}/graphql"))
        .bearer_auth(&token)
        .json(&json!({
            "query": "mutation($data: Json!) { createTestOrders(data: $data) { id status version } }",
            "variables": { "data": { "name": "First" } },
        }))
        .send()
        .await
        .unwrap()
        .json()
        .await
        .unwrap();
    assert!(create_res.get("errors").is_none(), "unexpected errors: {create_res:?}");
    let id = create_res["data"]["createTestOrders"]["id"]
        .as_str()
        .unwrap()
        .to_string();
    assert_eq!(create_res["data"]["createTestOrders"]["status"], "draft");
    let version = create_res["data"]["createTestOrders"]["version"].as_i64().unwrap();

    // get
    let get_res: serde_json::Value = client
        .post(format!("{base}/graphql"))
        .bearer_auth(&token)
        .json(&json!({
            "query": format!(r#"{{ testOrders(id: "{id}") {{ id capabilities }} }}"#),
        }))
        .send()
        .await
        .unwrap()
        .json()
        .await
        .unwrap();
    assert!(get_res.get("errors").is_none(), "unexpected errors: {get_res:?}");
    assert_eq!(get_res["data"]["testOrders"]["id"], id);
    assert_eq!(
        get_res["data"]["testOrders"]["capabilities"]["transitions"][0]["action"],
        "activate"
    );

    // transition
    let transition_res: serde_json::Value = client
        .post(format!("{base}/graphql"))
        .bearer_auth(&token)
        .json(&json!({
            "query": format!(
                r#"mutation {{ transitionTestOrders(id: "{id}", action: "activate", expectedVersion: {version}) {{ status version }} }}"#
            ),
        }))
        .send()
        .await
        .unwrap()
        .json()
        .await
        .unwrap();
    assert!(
        transition_res.get("errors").is_none(),
        "unexpected errors: {transition_res:?}"
    );
    assert_eq!(transition_res["data"]["transitionTestOrders"]["status"], "active");
    let version = transition_res["data"]["transitionTestOrders"]["version"]
        .as_i64()
        .unwrap();

    // stale-version update -> a GraphQL error carrying the same `version_conflict` code REST's
    // error envelope used, now in `extensions` instead of an `{"error": ...}` body.
    let conflict_res: serde_json::Value = client
        .post(format!("{base}/graphql"))
        .bearer_auth(&token)
        .json(&json!({
            "query": format!(
                r#"mutation($data: Json!) {{ updateTestOrders(id: "{id}", expectedVersion: 999, data: $data) {{ id }} }}"#
            ),
            "variables": { "data": { "name": "Changed" } },
        }))
        .send()
        .await
        .unwrap()
        .json()
        .await
        .unwrap();
    assert_eq!(conflict_res["errors"][0]["extensions"]["code"], "version_conflict");

    // delete
    let delete_res: serde_json::Value = client
        .post(format!("{base}/graphql"))
        .bearer_auth(&token)
        .json(&json!({
            "query": format!(
                r#"mutation {{ deleteTestOrders(id: "{id}", expectedVersion: {version}) {{ id }} }}"#
            ),
        }))
        .send()
        .await
        .unwrap()
        .json()
        .await
        .unwrap();
    assert!(delete_res.get("errors").is_none(), "unexpected errors: {delete_res:?}");

    // post-delete get -> GraphQL error, extensions.status carrying the original 404
    let after_delete: serde_json::Value = client
        .post(format!("{base}/graphql"))
        .bearer_auth(&token)
        .json(&json!({
            "query": format!(r#"{{ testOrders(id: "{id}") {{ id }} }}"#),
        }))
        .send()
        .await
        .unwrap()
        .json()
        .await
        .unwrap();
    assert_eq!(after_delete["errors"][0]["extensions"]["status"], 404);

    sqlx::query("DELETE FROM outbox_events WHERE aggregate_type = 'test.orders'")
        .execute(&pool)
        .await
        .ok();
    sqlx::query("DELETE FROM workflow_events WHERE tenant_id = $1")
        .bind(tenant_id)
        .execute(&pool)
        .await
        .ok();
    sqlx::query(&format!("DELETE FROM {TEST_ORDERS_TABLE} WHERE tenant_id = $1"))
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
        storage: None,
        min: None,
        max: None,
        min_length: None,
        max_length: None,
        computed: None,
    }
}

/// Dedicated table per entity name (`entities.test_<name-with-dots-as-underscores>`) — a caller
/// registering more than one entity via this helper (both tests below register `test.profiles`
/// *and* `test.tasks`) needs each on its own physical table now that the shared `records` table
/// this used to point at is gone (`crates/migrations/0033_drop_records_table.sql`); the
/// `entity = '...'` discriminator column that used to separate them within one table no longer
/// exists either.
fn dedicated_table_for(entity_name: &str) -> String {
    format!("entities.test_{}", entity_name.replace(['.', '-'], "_"))
}

fn plain_string_entity(name: &str, field_names: &[&str]) -> EntityDefinition {
    EntityDefinition {
        name: name.to_string(),
        label: name.to_string(),
        table_name: dedicated_table_for(name),
        fields: field_names.iter().map(|f| string_field(f)).collect(),
        list_views: vec![],
        workflow: None,
        unique_constraints: vec![],
        audit: None,
    }
}

async fn create_dedicated_table(pool: &PgPool, table_name: &str) {
    sqlx::query("CREATE SCHEMA IF NOT EXISTS entities")
        .execute(pool)
        .await
        .unwrap();
    sqlx::query(&format!(
        "CREATE TABLE IF NOT EXISTS {table_name} (
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

/// Reads `test.tasks` over `/graphql` (entity access is GraphQL-only — see `routes/records.rs`'s
/// removal) and translates the result back into REST's old status-code shape (200 on success, the
/// `extensions.status` carried by a GraphQL error otherwise), so the ABAC assertions below read
/// the same way they did before the REST `/api/test.tasks/{id}` route existed.
async fn read_test_task_status(client: &reqwest::Client, base: &str, token: &str, id: &str) -> i64 {
    let res: serde_json::Value = client
        .post(format!("{base}/graphql"))
        .bearer_auth(token)
        .json(&json!({ "query": format!(r#"{{ testTasks(id: "{id}") {{ id }} }}"#) }))
        .send()
        .await
        .unwrap()
        .json()
        .await
        .unwrap();
    match res.get("errors") {
        Some(errors) => errors[0]["extensions"]["status"].as_i64().unwrap(),
        None => 200,
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

    let profiles_table = dedicated_table_for("test.profiles");
    let tasks_table = dedicated_table_for("test.tasks");
    create_dedicated_table(&pool, &profiles_table).await;
    create_dedicated_table(&pool, &tasks_table).await;

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
    let graphql_routes = metap_graphql_http::router(&state, metap_graphql::SchemaLimits::default()).unwrap();
    let router = build_router(state, &[], graphql_routes);

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
    sqlx::query(&format!(
        "INSERT INTO {profiles_table} (tenant_id, data, version) VALUES ($1, $2, 1)"
    ))
    .bind(tenant_id)
    .bind(json!({ "userId": employee_user_id.to_string(), "deptId": "eng" }))
    .execute(&pool)
    .await
    .unwrap();

    // admin bypasses policy checks entirely -> seed two task records in different departments
    let eng_task: serde_json::Value = client
        .post(format!("{base}/graphql"))
        .bearer_auth(&admin_token)
        .json(&json!({
            "query": "mutation($data: Json!) { createTestTasks(data: $data) { id } }",
            "variables": { "data": { "deptId": "eng", "title": "Eng task" } },
        }))
        .send()
        .await
        .unwrap()
        .json()
        .await
        .unwrap();
    assert!(eng_task.get("errors").is_none(), "unexpected errors: {eng_task:?}");
    let eng_task_id = eng_task["data"]["createTestTasks"]["id"].as_str().unwrap().to_string();

    let sales_task: serde_json::Value = client
        .post(format!("{base}/graphql"))
        .bearer_auth(&admin_token)
        .json(&json!({
            "query": "mutation($data: Json!) { createTestTasks(data: $data) { id } }",
            "variables": { "data": { "deptId": "sales", "title": "Sales task" } },
        }))
        .send()
        .await
        .unwrap()
        .json()
        .await
        .unwrap();
    assert!(sales_task.get("errors").is_none(), "unexpected errors: {sales_task:?}");
    let sales_task_id = sales_task["data"]["createTestTasks"]["id"]
        .as_str()
        .unwrap()
        .to_string();

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
    let status = read_test_task_status(&client, &base, &employee_token, &eng_task_id).await;
    assert_eq!(status, 200);
    // ... but not the sales task — deny-by-default, no matching record-level policy.
    let status = read_test_task_status(&client, &base, &employee_token, &sales_task_id).await;
    assert_eq!(status, 403);

    // move the employee to "sales" ...
    sqlx::query(&format!(
        "UPDATE {profiles_table} SET data = jsonb_set(data, '{{deptId}}', '\"sales\"') \
         WHERE tenant_id = $1 AND data ->> 'userId' = $2"
    ))
    .bind(tenant_id)
    .bind(employee_user_id.to_string())
    .execute(&pool)
    .await
    .unwrap();

    // ... the cache still holds the stale "eng" attribute (long TTL, no invalidation yet) — the
    // employee's *next* request still resolves against the old department.
    let status = read_test_task_status(&client, &base, &employee_token, &eng_task_id).await;
    assert_eq!(
        status, 200,
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
    let status = read_test_task_status(&client, &base, &employee_token, &eng_task_id).await;
    assert_eq!(status, 403, "post-invalidate context should be fresh (deptId=sales)");
    let status = read_test_task_status(&client, &base, &employee_token, &sales_task_id).await;
    assert_eq!(status, 200, "post-invalidate context should now see the sales task");

    sqlx::query("DELETE FROM outbox_events WHERE aggregate_type = 'test.tasks'")
        .execute(&pool)
        .await
        .ok();
    sqlx::query("DELETE FROM policies WHERE tenant_id = $1")
        .bind(tenant_id)
        .execute(&pool)
        .await
        .ok();
    sqlx::query(&format!("DELETE FROM {tasks_table} WHERE tenant_id = $1"))
        .bind(tenant_id)
        .execute(&pool)
        .await
        .ok();
    sqlx::query(&format!("DELETE FROM {profiles_table} WHERE tenant_id = $1"))
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
