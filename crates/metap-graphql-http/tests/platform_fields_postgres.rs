//! E2E coverage for `metap-graphql-http::platform_fields` — the hand-written GraphQL fields that
//! replaced `metap-http`'s `routes::{admin,cron,dashboards,users}` REST route groups (2026-09-26,
//! `../metap-docs/docs/roadmap/95-platform-graphql-fields.md`). `routes::{platform_config,
//! tenant_config}`'s replacements (`platformConfig`/`tenantConfig`/`publicConfig` and friends) and
//! `routes::oauth2`'s 3 migrated admin routes are covered instead by
//! `../metap-http/tests/{platform_config_postgres,tenant_config_postgres,tenant_secret_postgres,
//! oauth2_authorization_server_postgres}.rs`, ported the same day — this file covers the
//! remaining groups those don't: users/roles, policies-as-admin-gate proof, cron jobs (including
//! the ported trigger/target validation), dashboards, and preferences.
//!
//! Harness mirrors `graphql_http_postgres.rs`, duplicated locally per this repo's convention.
//! `#[ignore]`d — needs `DATABASE_URL`.

use std::process::Command;
use std::sync::Arc;

use arc_swap::ArcSwap;
use jsonwebtoken::DecodingKey;
use metap_graphql::SchemaLimits;
use metap_http::{build_router, AppState};
use metap_metadata::MetadataRegistry;
use metap_permission::PermissionService;
use serde_json::{json, Value};
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
impl Drop for TempDir {
    fn drop(&mut self) {
        std::fs::remove_dir_all(&self.0).ok();
    }
}

fn openssl_genrsa() -> (TempDir, String, String) {
    let dir = std::env::temp_dir().join(format!("metap-graphql-http-platform-fields-{}", Uuid::new_v4()));
    std::fs::create_dir_all(&dir).unwrap();
    let private_path = dir.join("private.pem");
    let public_path = dir.join("public.pem");
    assert!(Command::new("openssl")
        .args(["genrsa", "-out"])
        .arg(&private_path)
        .arg("2048")
        .status()
        .unwrap()
        .success());
    assert!(Command::new("openssl")
        .args(["rsa", "-in"])
        .arg(&private_path)
        .args(["-pubout", "-out"])
        .arg(&public_path)
        .status()
        .unwrap()
        .success());
    let private = std::fs::read_to_string(private_path).unwrap();
    let public = std::fs::read_to_string(public_path).unwrap();
    (TempDir(dir), private, public)
}

struct TestServer {
    base: String,
    pool: PgPool,
    tenant_id: Uuid,
    admin_token: String,
    member_token: String,
    _keys: TempDir,
}

async fn boot_server() -> TestServer {
    let database_url = std::env::var("DATABASE_URL").expect("DATABASE_URL required for this e2e test");
    let pool = PgPoolOptions::new()
        .max_connections(5)
        .connect(&database_url)
        .await
        .unwrap();
    let (keys, private_pem, public_pem) = openssl_genrsa();

    let tenant_id = Uuid::new_v4();
    let (admin_id, member_id) = (Uuid::new_v4(), Uuid::new_v4());
    sqlx::query("INSERT INTO user_roles (tenant_id, user_id, role) VALUES ($1, $2, 'admin')")
        .bind(tenant_id)
        .bind(admin_id)
        .execute(&pool)
        .await
        .unwrap();
    sqlx::query("INSERT INTO user_roles (tenant_id, user_id, role) VALUES ($1, $2, 'member')")
        .bind(tenant_id)
        .bind(member_id)
        .execute(&pool)
        .await
        .unwrap();

    let registry = Arc::new(MetadataRegistry::new());
    let permissions = PermissionService::new(Box::new(metap_control::PostgresPolicyStore::new(test_router(
        pool.clone(),
    ))));
    let state = AppState::new(
        pool.clone(),
        registry.clone(),
        Arc::new(ArcSwap::new(registry)),
        Arc::new(permissions),
        DecodingKey::from_rsa_pem(public_pem.as_bytes()).unwrap(),
        private_pem.clone(),
        test_router(pool.clone()),
    );
    state.config.reload().await.unwrap();
    let graphql_routes = metap_graphql_http::router(&state, SchemaLimits::default()).unwrap();
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

    TestServer {
        base: format!("http://{addr}"),
        admin_token: metap_peripherals::mint_jwt(&private_pem, tenant_id, admin_id, 3600).unwrap(),
        member_token: metap_peripherals::mint_jwt(&private_pem, tenant_id, member_id, 3600).unwrap(),
        tenant_id,
        pool,
        _keys: keys,
    }
}

async fn cleanup(server: &TestServer) {
    for table in [
        "user_roles",
        "users",
        "cron_jobs",
        "cron_job_runs",
        "dashboard_configs",
        "user_preferences",
    ] {
        sqlx::query(&format!("DELETE FROM {table} WHERE tenant_id = $1"))
            .bind(server.tenant_id)
            .execute(&server.pool)
            .await
            .ok();
    }
}

async fn graphql(server: &TestServer, token: &str, query: &str, variables: Value) -> Value {
    reqwest::Client::new()
        .post(format!("{}/graphql", server.base))
        .bearer_auth(token)
        .json(&json!({ "query": query, "variables": variables }))
        .send()
        .await
        .unwrap()
        .json()
        .await
        .unwrap()
}

fn assert_no_errors(res: &Value) {
    assert!(res.get("errors").is_none(), "unexpected errors: {res:?}");
}

fn first_error(res: &Value) -> &Value {
    res["errors"]
        .as_array()
        .and_then(|e| e.first())
        .unwrap_or_else(|| panic!("expected a GraphQL error: {res:?}"))
}

/// Users/roles: `createAdminUser`/`assignUserRole`/`revokeUserRole`/`adminUsers`/`tenantUsers`,
/// plus the admin gate on all of them.
#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn users_and_roles_lifecycle() {
    let server = boot_server().await;

    // A member cannot create a user — `require_admin`'s `AdminContext` equivalent.
    let denied = graphql(
        &server,
        &server.member_token,
        "mutation($email: String!, $password: String!) { createAdminUser(email: $email, password: $password) }",
        json!({ "email": "denied@example.com", "password": "hunter2hunter2" }),
    )
    .await;
    assert_eq!(first_error(&denied)["extensions"]["status"], 403);

    // `users.email` is unique across the whole table, not per-tenant — randomized so reruns of
    // this test never collide with a previous run's row.
    let email = format!("newuser-{}@example.com", Uuid::new_v4());
    let created = graphql(
        &server,
        &server.admin_token,
        "mutation($email: String!, $password: String!, $roles: [String!]) { \
            createAdminUser(email: $email, password: $password, roles: $roles) }",
        json!({ "email": email, "password": "hunter2hunter2", "roles": ["member"] }),
    )
    .await;
    assert_no_errors(&created);
    let user_id = created["data"]["createAdminUser"]["userId"]
        .as_str()
        .unwrap()
        .to_string();
    assert_eq!(created["data"]["createAdminUser"]["roles"], json!(["member"]));

    // `adminUsers` lists the role assignment.
    let listed = graphql(&server, &server.admin_token, "{ adminUsers }", json!({})).await;
    assert_no_errors(&listed);
    let entry = listed["data"]["adminUsers"]
        .as_array()
        .unwrap()
        .iter()
        .find(|u| u["userId"] == user_id)
        .expect("the created user is listed");
    assert_eq!(entry["roles"], json!(["member"]));

    // Assign a second role, then revoke the first — `tenantUsers` (the plain picker) also sees
    // this user regardless of role changes.
    let assigned = graphql(
        &server,
        &server.admin_token,
        "mutation($userId: ID!, $role: String!) { assignUserRole(userId: $userId, role: $role) }",
        json!({ "userId": user_id, "role": "admin" }),
    )
    .await;
    assert_no_errors(&assigned);
    assert_eq!(assigned["data"]["assignUserRole"], true);

    let revoked = graphql(
        &server,
        &server.admin_token,
        "mutation($userId: ID!, $role: String!) { revokeUserRole(userId: $userId, role: $role) }",
        json!({ "userId": user_id, "role": "member" }),
    )
    .await;
    assert_no_errors(&revoked);
    assert_eq!(revoked["data"]["revokeUserRole"], true);

    let listed = graphql(&server, &server.admin_token, "{ adminUsers }", json!({})).await;
    let entry = listed["data"]["adminUsers"]
        .as_array()
        .unwrap()
        .iter()
        .find(|u| u["userId"] == user_id)
        .unwrap();
    assert_eq!(entry["roles"], json!(["admin"]));

    let picker = graphql(&server, &server.member_token, "{ tenantUsers }", json!({})).await;
    assert_no_errors(&picker);
    assert!(picker["data"]["tenantUsers"]
        .as_array()
        .unwrap()
        .iter()
        .any(|u| u["id"] == user_id));

    let invalidated = graphql(
        &server,
        &server.admin_token,
        "mutation($userId: ID!) { invalidateUserContextCache(userId: $userId) }",
        json!({ "userId": user_id }),
    )
    .await;
    assert_no_errors(&invalidated);
    assert_eq!(invalidated["data"]["invalidateUserContextCache"], true);

    cleanup(&server).await;
}

/// Cron jobs: create/list/get/update/delete, plus the ported trigger/target validation actually
/// rejecting a malformed job instead of silently accepting it.
#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn cron_job_lifecycle_and_validation() {
    let server = boot_server().await;

    // A schedule job with no cronExpr is refused before ever reaching the database.
    let rejected = graphql(
        &server,
        &server.admin_token,
        "mutation($input: Json!) { createCronJob(input: $input) }",
        json!({ "input": { "name": "bad", "targetType": "webhook", "targetConfig": { "url": "https://example.com" } } }),
    )
    .await;
    assert_eq!(first_error(&rejected)["extensions"]["code"], "validation_failed");

    let created = graphql(
        &server,
        &server.admin_token,
        "mutation($input: Json!) { createCronJob(input: $input) }",
        json!({
            "input": {
                "name": "nightly webhook",
                "cronExpr": "0 0 0 * * *",
                "targetType": "webhook",
                "targetConfig": { "url": "https://example.com/hook" },
            },
        }),
    )
    .await;
    assert_no_errors(&created);
    let job_id = created["data"]["createCronJob"]["id"].as_str().unwrap().to_string();
    assert_eq!(created["data"]["createCronJob"]["enabled"], true);

    let listed = graphql(&server, &server.admin_token, "{ cronJobs }", json!({})).await;
    assert_no_errors(&listed);
    assert!(listed["data"]["cronJobs"]
        .as_array()
        .unwrap()
        .iter()
        .any(|j| j["id"] == job_id));

    let fetched = graphql(
        &server,
        &server.admin_token,
        "query($id: ID!) { cronJob(id: $id) }",
        json!({ "id": job_id }),
    )
    .await;
    assert_no_errors(&fetched);
    assert_eq!(fetched["data"]["cronJob"]["name"], "nightly webhook");

    let updated = graphql(
        &server,
        &server.admin_token,
        "mutation($id: ID!, $input: Json!) { updateCronJob(id: $id, input: $input) }",
        json!({ "id": job_id, "input": { "enabled": false } }),
    )
    .await;
    assert_no_errors(&updated);
    assert_eq!(updated["data"]["updateCronJob"]["enabled"], false);

    let runs = graphql(
        &server,
        &server.admin_token,
        "query($id: ID!) { cronJobRuns(id: $id) }",
        json!({ "id": job_id }),
    )
    .await;
    assert_no_errors(&runs);
    assert_eq!(runs["data"]["cronJobRuns"], json!([]), "a fresh job has no runs yet");

    let deleted = graphql(
        &server,
        &server.admin_token,
        "mutation($id: ID!) { deleteCronJob(id: $id) }",
        json!({ "id": job_id }),
    )
    .await;
    assert_no_errors(&deleted);
    assert_eq!(deleted["data"]["deleteCronJob"], true);

    // `cronJob(id)` is a nullable find-by-id field — a deleted/unknown id is `null`, not an error
    // (`updateCronJob`/`deleteCronJob` on an unknown id *do* error, since a mutation targeting
    // nothing is a real failure; a lookup finding nothing is normal).
    let gone = graphql(
        &server,
        &server.admin_token,
        "query($id: ID!) { cronJob(id: $id) }",
        json!({ "id": job_id }),
    )
    .await;
    assert_no_errors(&gone);
    assert_eq!(gone["data"]["cronJob"], Value::Null);

    let update_missing = graphql(
        &server,
        &server.admin_token,
        "mutation($id: ID!) { updateCronJob(id: $id, input: {}) }",
        json!({ "id": job_id }),
    )
    .await;
    assert_eq!(first_error(&update_missing)["extensions"]["code"], "cron_job_not_found");

    cleanup(&server).await;
}

/// Dashboards: personal layout (any authenticated user) vs. tenant default (admin-only write),
/// and the effective-dashboard fallback from personal to tenant-default.
#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn dashboard_personal_and_tenant_default() {
    let server = boot_server().await;

    // Nothing set yet.
    let empty = graphql(&server, &server.member_token, "{ myDashboard }", json!({})).await;
    assert_no_errors(&empty);
    assert_eq!(empty["data"]["myDashboard"], Value::Null);

    // A member cannot set the tenant default.
    let denied = graphql(
        &server,
        &server.member_token,
        "mutation($layout: Json!) { setTenantDefaultDashboard(layout: $layout) }",
        json!({ "layout": { "widgets": ["fleet-wide"] } }),
    )
    .await;
    assert_eq!(first_error(&denied)["extensions"]["status"], 403);

    let set_default = graphql(
        &server,
        &server.admin_token,
        "mutation($layout: Json!) { setTenantDefaultDashboard(layout: $layout) }",
        json!({ "layout": { "widgets": ["fleet-wide"] } }),
    )
    .await;
    assert_no_errors(&set_default);

    // The member, who has no personal layout, now falls back to the tenant default.
    let effective = graphql(&server, &server.member_token, "{ myDashboard }", json!({})).await;
    assert_no_errors(&effective);
    assert_eq!(
        effective["data"]["myDashboard"]["layout"],
        json!({ "widgets": ["fleet-wide"] })
    );

    // Setting a personal layout overrides the fallback for that user only.
    let set_personal = graphql(
        &server,
        &server.member_token,
        "mutation($layout: Json!) { setMyDashboard(layout: $layout) }",
        json!({ "layout": { "widgets": ["personal"] } }),
    )
    .await;
    assert_no_errors(&set_personal);
    let effective = graphql(&server, &server.member_token, "{ myDashboard }", json!({})).await;
    assert_eq!(
        effective["data"]["myDashboard"]["layout"],
        json!({ "widgets": ["personal"] })
    );

    let tenant_default = graphql(&server, &server.admin_token, "{ tenantDefaultDashboard }", json!({})).await;
    assert_eq!(
        tenant_default["data"]["tenantDefaultDashboard"]["layout"],
        json!({ "widgets": ["fleet-wide"] }),
        "the admin's own read of the tenant default is unaffected by the member's personal override"
    );

    cleanup(&server).await;
}

/// Preferences: locale round trip plus the allowlist validation.
#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn preferences_locale_round_trip() {
    let server = boot_server().await;

    let default = graphql(&server, &server.member_token, "{ myPreferences }", json!({})).await;
    assert_no_errors(&default);
    assert_eq!(default["data"]["myPreferences"]["locale"], "en");

    let rejected = graphql(
        &server,
        &server.member_token,
        "mutation($locale: String!) { setMyPreferences(locale: $locale) }",
        json!({ "locale": "xx" }),
    )
    .await;
    assert_eq!(first_error(&rejected)["extensions"]["code"], "validation_failed");

    let set = graphql(
        &server,
        &server.member_token,
        "mutation($locale: String!) { setMyPreferences(locale: $locale) }",
        json!({ "locale": "vi" }),
    )
    .await;
    assert_no_errors(&set);
    assert_eq!(set["data"]["setMyPreferences"]["locale"], "vi");

    let read_back = graphql(&server, &server.member_token, "{ myPreferences }", json!({})).await;
    assert_eq!(read_back["data"]["myPreferences"]["locale"], "vi");

    cleanup(&server).await;
}
