//! Security regression suite (`testing/security/checklist.md`) — auth-boundary tests against a
//! real axum server + real RS256 JWTs (mirrors `http_server.rs`'s pattern, duplicated locally
//! rather than shared — every `*_postgres.rs` file in this repo already defines its own small
//! helpers instead of a common test-utils crate). `#[ignore]`d — see
//! `crates/metap-query/tests/query_planner_postgres.rs`'s doc comment for the convention.

use std::process::Command;
use std::sync::Arc;

use arc_swap::ArcSwap;
use axum::Router;
use jsonwebtoken::DecodingKey;
use metap_http::{build_router, AppState};
use metap_metadata::{EntityDefinition, EntityField, EntityListView, FieldKind, MetadataRegistry};
use metap_permission::PermissionService;
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

fn mint_token_ttl(private_pem: &str, tenant_id: Uuid, user_id: Uuid, ttl_seconds: u64) -> String {
    metap_peripherals::mint_jwt(private_pem, tenant_id, user_id, ttl_seconds).unwrap()
}

fn test_entity() -> EntityDefinition {
    EntityDefinition {
        name: "test.jwt_orders".to_string(),
        label: "Order".to_string(),
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
        }],
        list_views: vec![EntityListView {
            name: "default".to_string(),
            label: "Default".to_string(),
            fields: vec!["name".to_string()],
            filters: vec![],
            default_sort: Some("-createdAt".to_string()),
            max_limit: 50,
        }],
        workflow: None,
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

struct TempDir(std::path::PathBuf);
impl TempDir {
    fn new() -> Self {
        let path = std::env::temp_dir().join(format!("metap-http-jwt-test-{}", Uuid::new_v4()));
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

/// Boots one real server on its own keypair and returns everything a test needs to hit it —
/// shared setup for every scenario below, none of which touch the server's *own* wiring, only
/// what kind of token they send it.
struct TestServer {
    base: String,
    private_pem: String,
    pool: PgPool,
}

async fn boot_server(tenant_id: Uuid, user_id: Uuid) -> TestServer {
    let pool = connect().await;
    sqlx::query("INSERT INTO user_roles (tenant_id, user_id, role) VALUES ($1, $2, 'admin')")
        .bind(tenant_id)
        .bind(user_id)
        .execute(&pool)
        .await
        .unwrap();

    let keydir = TempDir::new();
    let (private_pem, public_pem) = openssl_genrsa(keydir.path());

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
    let router = build_router(state, &["http://localhost:5173".to_string()], Router::new());
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
        private_pem,
        pool,
    }
}

async fn cleanup(pool: &PgPool, tenant_id: Uuid) {
    sqlx::query("DELETE FROM records WHERE tenant_id = $1")
        .bind(tenant_id)
        .execute(pool)
        .await
        .ok();
    sqlx::query("DELETE FROM user_roles WHERE tenant_id = $1")
        .bind(tenant_id)
        .execute(pool)
        .await
        .ok();
    sqlx::query("DELETE FROM tenant_auth_configs WHERE tenant_id = $1")
        .bind(tenant_id)
        .execute(pool)
        .await
        .ok();
    sqlx::query("DELETE FROM users WHERE tenant_id = $1")
        .bind(tenant_id)
        .execute(pool)
        .await
        .ok();
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn missing_token_is_rejected() {
    let tenant_id = Uuid::new_v4();
    let user_id = Uuid::new_v4();
    let server = boot_server(tenant_id, user_id).await;

    let res = reqwest::Client::new()
        .get(format!("{}/api/test.jwt_orders", server.base))
        .send()
        .await
        .unwrap();
    assert_eq!(res.status(), 401);

    cleanup(&server.pool, tenant_id).await;
}

/// `auth.rs` sets `Validation::leeway = 20` explicitly (tightened 2026-08-24 from the
/// `jsonwebtoken` crate's 60s default — clock-skew tolerance, not a bug: an earlier version of
/// this test slept only 2s past `exp` and got a `200`, not a `401`, because 2s was well inside
/// the (then 60s) window). Sleeping past the leeway, not just past `exp`, is what actually
/// exercises rejection for *this* server's real configured behavior.
#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn expired_token_is_rejected() {
    let tenant_id = Uuid::new_v4();
    let user_id = Uuid::new_v4();
    let server = boot_server(tenant_id, user_id).await;

    let token = mint_token_ttl(&server.private_pem, tenant_id, user_id, 1);
    tokio::time::sleep(std::time::Duration::from_secs(25)).await;

    let res = reqwest::Client::new()
        .get(format!("{}/api/test.jwt_orders", server.base))
        .bearer_auth(&token)
        .send()
        .await
        .unwrap();
    assert_eq!(res.status(), 401, "a token past exp + the 20s leeway must be rejected");

    cleanup(&server.pool, tenant_id).await;
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn tampered_signature_is_rejected() {
    let tenant_id = Uuid::new_v4();
    let user_id = Uuid::new_v4();
    let server = boot_server(tenant_id, user_id).await;

    let token = mint_token_ttl(&server.private_pem, tenant_id, user_id, 3600);
    let mut parts: Vec<&str> = token.split('.').collect();
    assert_eq!(parts.len(), 3, "JWT must have 3 segments");
    // Flip the signature segment's first character — any single-byte change invalidates an
    // RS256 signature (it isn't malleable), so this always breaks verification cleanly.
    let mut sig = parts[2].to_string();
    let first = sig.chars().next().unwrap();
    let replacement = if first == 'A' { 'B' } else { 'A' };
    sig.replace_range(0..1, &replacement.to_string());
    parts[2] = &sig;
    let tampered = parts.join(".");

    let res = reqwest::Client::new()
        .get(format!("{}/api/test.jwt_orders", server.base))
        .bearer_auth(&tampered)
        .send()
        .await
        .unwrap();
    assert_eq!(res.status(), 401, "a tampered signature must be rejected");

    cleanup(&server.pool, tenant_id).await;
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn token_signed_by_a_different_key_is_rejected() {
    let tenant_id = Uuid::new_v4();
    let user_id = Uuid::new_v4();
    let server = boot_server(tenant_id, user_id).await;

    // A second, entirely unrelated RSA keypair — this token is well-formed and its claims are
    // legitimate, it's just signed by a key the server never issued and doesn't trust.
    let attacker_keydir = TempDir::new();
    let (attacker_private_pem, _attacker_public_pem) = openssl_genrsa(attacker_keydir.path());
    let forged = mint_token_ttl(&attacker_private_pem, tenant_id, user_id, 3600);

    let res = reqwest::Client::new()
        .get(format!("{}/api/test.jwt_orders", server.base))
        .bearer_auth(&forged)
        .send()
        .await
        .unwrap();
    assert_eq!(res.status(), 401, "a token signed by an untrusted key must be rejected");

    cleanup(&server.pool, tenant_id).await;
}

/// A perfectly valid, correctly-signed token for tenant B must never see tenant A's data, even
/// though both tokens are minted by the same server key and both pass signature verification —
/// the isolation has to come from `tenant_id` scoping downstream of auth, not from auth itself.
#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn a_valid_token_for_one_tenant_cannot_read_another_tenants_record() {
    let tenant_a = Uuid::new_v4();
    let user_a = Uuid::new_v4();
    let server = boot_server(tenant_a, user_a).await;

    let tenant_b = Uuid::new_v4();
    let user_b = Uuid::new_v4();
    sqlx::query("INSERT INTO user_roles (tenant_id, user_id, role) VALUES ($1, $2, 'admin')")
        .bind(tenant_b)
        .bind(user_b)
        .execute(&server.pool)
        .await
        .unwrap();

    let token_a = mint_token_ttl(&server.private_pem, tenant_a, user_a, 3600);
    let token_b = mint_token_ttl(&server.private_pem, tenant_b, user_b, 3600);

    let client = reqwest::Client::new();
    let create_res = client
        .post(format!("{}/api/test.jwt_orders", server.base))
        .bearer_auth(&token_a)
        .json(&json!({ "data": { "name": "tenant-a-secret" } }))
        .send()
        .await
        .unwrap();
    assert_eq!(create_res.status(), 201);
    let created: serde_json::Value = create_res.json().await.unwrap();
    let id = created["data"]["id"].as_str().unwrap();

    let get_as_b = client
        .get(format!("{}/api/test.jwt_orders/{id}", server.base))
        .bearer_auth(&token_b)
        .send()
        .await
        .unwrap();
    assert_eq!(
        get_as_b.status(),
        404,
        "tenant B must not be able to fetch tenant A's record by id"
    );

    let list_as_b = client
        .get(format!("{}/api/test.jwt_orders", server.base))
        .bearer_auth(&token_b)
        .send()
        .await
        .unwrap();
    assert_eq!(list_as_b.status(), 200);
    let listed: serde_json::Value = list_as_b.json().await.unwrap();
    assert_eq!(
        listed["data"].as_array().map(|a| a.len()),
        Some(0),
        "tenant B's list must not include tenant A's record"
    );

    cleanup(&server.pool, tenant_a).await;
    cleanup(&server.pool, tenant_b).await;
}

/// Regression for the finding in `AUDIT_2.md`: `basic_auth` (`crates/metap-http/src/auth.rs`)
/// trusted the caller-supplied `X-Tenant-Id` header outright once the password verified, never
/// checking it against the user's real `tenant_id` — a valid password for tenant A's user plus
/// `X-Tenant-Id: <tenant B>` used to authenticate the request *as* tenant B. `users.email` has a
/// global unique index (no `tenant_id` component), so a correct password can only ever belong to
/// one specific tenant; declaring a different one must be rejected outright, not silently
/// corrected to the real one (`POST /auth/login` gets to do that because it mints the token for
/// `user.tenant_id`; `basic_auth` returns the header value directly into `RequestContext`, so it
/// has to refuse instead).
#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn basic_auth_rejects_a_declared_tenant_that_does_not_match_the_users_real_tenant() {
    let tenant_a = Uuid::new_v4();
    let user_a = Uuid::new_v4();
    let server = boot_server(tenant_a, user_a).await;
    let tenant_b = Uuid::new_v4();

    let email = format!("{}@example.test", Uuid::new_v4());
    let password = "correct-horse-battery-staple";
    let user = metap_peripherals::create_user(&server.pool, tenant_a, &email, password)
        .await
        .unwrap();
    sqlx::query("INSERT INTO user_roles (tenant_id, user_id, role) VALUES ($1, $2, 'admin')")
        .bind(tenant_a)
        .bind(user.id)
        .execute(&server.pool)
        .await
        .unwrap();
    for tenant in [tenant_a, tenant_b] {
        sqlx::query("INSERT INTO tenant_auth_configs (tenant_id, provider_kind, enabled) VALUES ($1, 'basic', true)")
            .bind(tenant)
            .execute(&server.pool)
            .await
            .unwrap();
    }

    let client = reqwest::Client::new();

    // Correct password, but declaring tenant B (not this user's real tenant A) — must be
    // rejected, not silently authenticated as tenant B.
    let cross_tenant = client
        .get(format!("{}/api/test.jwt_orders", server.base))
        .basic_auth(&email, Some(password))
        .header("X-Tenant-Id", tenant_b.to_string())
        .send()
        .await
        .unwrap();
    assert_eq!(
        cross_tenant.status(),
        401,
        "a valid password declaring the wrong tenant must not authenticate as that tenant"
    );

    // Sanity: the same credential against its *real* tenant still works — the fix must not have
    // broken the legitimate path.
    let same_tenant = client
        .get(format!("{}/api/test.jwt_orders", server.base))
        .basic_auth(&email, Some(password))
        .header("X-Tenant-Id", tenant_a.to_string())
        .send()
        .await
        .unwrap();
    assert_eq!(
        same_tenant.status(),
        200,
        "the user's own real tenant must still authenticate"
    );

    cleanup(&server.pool, tenant_a).await;
    cleanup(&server.pool, tenant_b).await;
}
