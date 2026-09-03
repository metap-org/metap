//! Cookie-session + CSRF regression suite — the coverage gap audit 04 finding B#4 found
//! (`../metap-docs/docs/audits/04-auth-protocols-gateway-audit.md`): the whole mechanism shipped
//! 2026-09-03 with `metap_session`/`x-csrf-token` appearing in **no** test in the workspace, even
//! though `crates/metap-http` already had `jwt_security_postgres.rs` for exactly this kind of
//! auth-boundary check.
//!
//! `crates/metap-http/src/cookies.rs`'s own unit tests cover the two pure decisions
//! (`requires_csrf_check`/`csrf_matches`) and the cookie attributes without needing a database.
//! What *this* file adds is the part those can't reach: that `AuthContext`'s extractor actually
//! consults them, in the right order, on a real server over real HTTP — a cookie alone
//! authenticates a safe request, a mutating one needs the matching header, and an `Authorization`
//! header still wins outright and is never CSRF-gated.
//!
//! Harness (keypair, entity, server boot, cleanup) mirrors `jwt_security_postgres.rs` and is
//! duplicated locally on purpose — every `*_postgres.rs` in this repo defines its own small
//! helpers rather than sharing a test-utils crate. `#[ignore]`d, same convention as the rest.

use std::process::Command;
use std::sync::Arc;

use arc_swap::ArcSwap;
use axum::Router;
use jsonwebtoken::DecodingKey;
use metap_http::cookies::{CSRF_COOKIE_NAME, CSRF_HEADER_NAME, SESSION_COOKIE_NAME};
use metap_http::{build_router, AppState};
use metap_metadata::{EntityDefinition, EntityField, EntityListView, FieldKind, MetadataRegistry};
use metap_permission::PermissionService;
use serde_json::json;
use sqlx::postgres::PgPoolOptions;
use sqlx::PgPool;
use uuid::Uuid;

const ENTITY: &str = "test.cookie_orders";
const CSRF_VALUE: &str = "csrf-token-value";

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

fn test_entity() -> EntityDefinition {
    EntityDefinition {
        name: ENTITY.to_string(),
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
            default_sort: Some("-createdAt".to_string()),
            max_limit: 50,
        }],
        workflow: None,
    }
}

struct TempDir(std::path::PathBuf);
impl TempDir {
    fn new() -> Self {
        let path = std::env::temp_dir().join(format!("metap-http-cookie-test-{}", Uuid::new_v4()));
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

struct TestServer {
    base: String,
    token: String,
    pool: PgPool,
}

async fn boot_server(tenant_id: Uuid, user_id: Uuid) -> TestServer {
    let database_url = std::env::var("DATABASE_URL").expect("DATABASE_URL required for this e2e test");
    let pool = PgPoolOptions::new()
        .max_connections(5)
        .connect(&database_url)
        .await
        .unwrap();
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

    let token = metap_peripherals::mint_jwt(&private_pem, tenant_id, user_id, 3600).unwrap();
    TestServer {
        base: format!("http://{addr}"),
        token,
        pool,
    }
}

async fn cleanup(pool: &PgPool, tenant_id: Uuid) {
    for sql in [
        "DELETE FROM records WHERE tenant_id = $1",
        "DELETE FROM user_roles WHERE tenant_id = $1",
        "DELETE FROM users WHERE tenant_id = $1",
    ] {
        sqlx::query(sql).bind(tenant_id).execute(pool).await.ok();
    }
}

/// The `Cookie` header a browser would send after a real login: session JWT + CSRF value.
fn session_cookie_header(token: &str) -> String {
    format!("{SESSION_COOKIE_NAME}={token}; {CSRF_COOKIE_NAME}={CSRF_VALUE}")
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn a_session_cookie_alone_authenticates_a_safe_request() {
    let tenant_id = Uuid::new_v4();
    let user_id = Uuid::new_v4();
    let server = boot_server(tenant_id, user_id).await;

    let res = reqwest::Client::new()
        .get(format!("{}/api/{ENTITY}", server.base))
        .header("cookie", session_cookie_header(&server.token))
        .send()
        .await
        .unwrap();

    assert_eq!(res.status(), 200, "a GET needs no CSRF header");
    cleanup(&server.pool, tenant_id).await;
}

/// The single most important assertion in this file: without it, an inverted or widened
/// `requires_csrf_check` would leave the cookie path open to cross-site state change and every
/// other test here would still pass.
#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn a_mutating_request_without_the_csrf_header_is_rejected() {
    let tenant_id = Uuid::new_v4();
    let user_id = Uuid::new_v4();
    let server = boot_server(tenant_id, user_id).await;

    let res = reqwest::Client::new()
        .post(format!("{}/api/{ENTITY}", server.base))
        .header("cookie", session_cookie_header(&server.token))
        .json(&json!({ "data": { "name": "Forged" } }))
        .send()
        .await
        .unwrap();

    assert_eq!(
        res.status(),
        401,
        "cookie-authenticated POST must require the CSRF header"
    );
    let body: serde_json::Value = res.json().await.unwrap();
    assert_eq!(body["error"]["code"], "unauthorized");
    cleanup(&server.pool, tenant_id).await;
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn a_mutating_request_with_a_mismatched_csrf_header_is_rejected() {
    let tenant_id = Uuid::new_v4();
    let user_id = Uuid::new_v4();
    let server = boot_server(tenant_id, user_id).await;

    let res = reqwest::Client::new()
        .post(format!("{}/api/{ENTITY}", server.base))
        .header("cookie", session_cookie_header(&server.token))
        .header(CSRF_HEADER_NAME, "not-the-cookie-value")
        .json(&json!({ "data": { "name": "Forged" } }))
        .send()
        .await
        .unwrap();

    assert_eq!(res.status(), 401);
    cleanup(&server.pool, tenant_id).await;
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn a_mutating_request_with_the_matching_csrf_header_succeeds() {
    let tenant_id = Uuid::new_v4();
    let user_id = Uuid::new_v4();
    let server = boot_server(tenant_id, user_id).await;

    let res = reqwest::Client::new()
        .post(format!("{}/api/{ENTITY}", server.base))
        .header("cookie", session_cookie_header(&server.token))
        .header(CSRF_HEADER_NAME, CSRF_VALUE)
        .json(&json!({ "data": { "name": "Legitimate" } }))
        .send()
        .await
        .unwrap();

    assert_eq!(res.status(), 201, "the double-submit pair must be accepted");
    cleanup(&server.pool, tenant_id).await;
}

/// Header-beats-cookie is what keeps the CSRF check from ever applying to a Bearer caller — a CLI,
/// a service, `dev-tools mint-token` output. A regression here would break every non-browser
/// client the moment it happened to also carry a cookie.
#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn an_authorization_header_wins_over_a_cookie_and_is_never_csrf_gated() {
    let tenant_id = Uuid::new_v4();
    let user_id = Uuid::new_v4();
    let server = boot_server(tenant_id, user_id).await;

    let res = reqwest::Client::new()
        .post(format!("{}/api/{ENTITY}", server.base))
        .bearer_auth(&server.token)
        // A junk session cookie and no CSRF header at all: if the extractor ever consulted the
        // cookie first, or ran the CSRF check on the header path, this would 401.
        .header("cookie", format!("{SESSION_COOKIE_NAME}=not-a-real-jwt"))
        .json(&json!({ "data": { "name": "Bearer client" } }))
        .send()
        .await
        .unwrap();

    assert_eq!(res.status(), 201);
    cleanup(&server.pool, tenant_id).await;
}

/// `POST /auth/logout` must send back both cookies with `Max-Age=0` — and must do so without
/// requiring auth, so a browser whose session already expired can still clear it.
#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn logout_clears_both_cookies_without_requiring_auth() {
    let tenant_id = Uuid::new_v4();
    let user_id = Uuid::new_v4();
    let server = boot_server(tenant_id, user_id).await;

    let res = reqwest::Client::new()
        .post(format!("{}/auth/logout", server.base))
        .send()
        .await
        .unwrap();

    assert_eq!(res.status(), 204);
    let set_cookies: Vec<String> = res
        .headers()
        .get_all("set-cookie")
        .iter()
        .map(|v| v.to_str().unwrap().to_string())
        .collect();
    assert_eq!(set_cookies.len(), 2, "both cookies must be cleared: {set_cookies:?}");
    assert!(set_cookies.iter().any(|c| c.starts_with(SESSION_COOKIE_NAME)));
    assert!(set_cookies.iter().any(|c| c.starts_with(CSRF_COOKIE_NAME)));
    for cookie in &set_cookies {
        assert!(cookie.contains("Max-Age=0"), "must expire immediately: {cookie}");
    }
    cleanup(&server.pool, tenant_id).await;
}

/// An expired session cookie must be rejected exactly like an expired Bearer token — the cookie is
/// only a transport for the same JWT, it is not a second, weaker credential.
#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn an_expired_token_in_a_cookie_is_rejected() {
    let tenant_id = Uuid::new_v4();
    let user_id = Uuid::new_v4();
    let server = boot_server(tenant_id, user_id).await;

    // Minted against a *different* keypair — the shape of "a cookie value this server will not
    // accept" that doesn't require waiting out a real expiry.
    let res = reqwest::Client::new()
        .get(format!("{}/api/{ENTITY}", server.base))
        .header("cookie", format!("{SESSION_COOKIE_NAME}=clearly.not.a.jwt"))
        .send()
        .await
        .unwrap();

    assert_eq!(res.status(), 401);
    cleanup(&server.pool, tenant_id).await;
}
