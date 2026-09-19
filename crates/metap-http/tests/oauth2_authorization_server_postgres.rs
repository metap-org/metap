//! Live end-to-end proof of `routes::oauth2`'s wiring over real HTTP against a real Postgres —
//! `metap-oauth-server/tests/oauth_server_postgres.rs` already proves the library's atomicity
//! guarantees directly; what this file adds is the part that can't: that `POST /oauth/token`'s
//! `Form` extractor, HTTP Basic client auth, `AuthContext`/`AdminContext` gating, and the actual
//! route registrations in `build_router` all line up. `#[ignore]`d, same convention as every
//! other e2e test in this repo. Harness mirrors `cookie_session_postgres.rs`.

use std::process::Command;
use std::sync::Arc;

use arc_swap::ArcSwap;
use axum::Router;
use jsonwebtoken::DecodingKey;
use metap_http::{build_router, AppState};
use metap_metadata::MetadataRegistry;
use metap_permission::PermissionService;
use serde_json::Value;
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
        .unwrap();
    assert!(status.success());
    let status = Command::new("openssl")
        .args(["rsa", "-in"])
        .arg(&private_path)
        .args(["-pubout", "-out"])
        .arg(&public_path)
        .status()
        .unwrap();
    assert!(status.success());
    (
        std::fs::read_to_string(private_path).unwrap(),
        std::fs::read_to_string(public_path).unwrap(),
    )
}

struct TempDir(std::path::PathBuf);
impl TempDir {
    fn new() -> Self {
        let path = std::env::temp_dir().join(format!("metap-http-oauth2-test-{}", Uuid::new_v4()));
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
    /// The resource owner's own session token — used to authenticate `GET /oauth/authorize` and,
    /// since this test's user also holds the `admin` role, `POST /admin/oauth/clients`.
    user_token: String,
    /// Kept so a test can mint a *second* user's session token (`metap_peripherals::mint_jwt`)
    /// against the same trust root this server verifies against, without re-booting a server.
    private_pem: String,
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

    let user_token = metap_peripherals::mint_jwt(&private_pem, tenant_id, user_id, 3600).unwrap();
    TestServer {
        base: format!("http://{addr}"),
        user_token,
        private_pem,
        pool,
    }
}

async fn cleanup(pool: &PgPool, tenant_id: Uuid) {
    for sql in [
        "DELETE FROM oauth_refresh_tokens WHERE tenant_id = $1",
        "DELETE FROM oauth_authorization_codes WHERE tenant_id = $1",
        "DELETE FROM oauth_consents WHERE tenant_id = $1",
        "DELETE FROM oauth_pending_authorizations WHERE tenant_id = $1",
        "DELETE FROM oauth_clients WHERE tenant_id = $1",
        "DELETE FROM user_roles WHERE tenant_id = $1",
        "DELETE FROM users WHERE tenant_id = $1",
    ] {
        sqlx::query(sql).bind(tenant_id).execute(pool).await.ok();
    }
}

/// Pulls the `requestId: "<uuid>"` the consent page's inline script embeds
/// (`routes::oauth2::consent_page_html`) out of the raw HTML body — this test's only way to drive
/// the approve/deny step without a real browser.
fn extract_request_id(html: &str) -> Uuid {
    let marker = "requestId: \"";
    let start = html.find(marker).expect("consent page must embed requestId") + marker.len();
    let end = html[start..].find('"').unwrap();
    Uuid::parse_str(&html[start..start + end]).unwrap()
}

/// Drives a fresh `GET /oauth/authorize` through to a redirect carrying `?code=...`, going
/// through the real consent screen (asserting it actually appeared) rather than assuming a prior
/// consent exists — the common path most tests below want.
async fn authorize_and_approve(
    server: &TestServer,
    client_id: &str,
    redirect_uri: &str,
    scope: &str,
    state_param: &str,
) -> String {
    let no_redirect = no_redirect_client();
    let authorize_res = no_redirect
        .get(format!("{}/oauth/authorize", server.base))
        .bearer_auth(&server.user_token)
        .query(&[
            ("response_type", "code"),
            ("client_id", client_id),
            ("redirect_uri", redirect_uri),
            ("scope", scope),
            ("state", state_param),
        ])
        .send()
        .await
        .unwrap();
    assert_eq!(
        authorize_res.status(),
        200,
        "a first-time request must show the consent screen"
    );
    let html = authorize_res.text().await.unwrap();
    let request_id = extract_request_id(&html);

    let decision_res = reqwest::Client::new()
        .post(format!("{}/oauth/authorize/decision", server.base))
        .bearer_auth(&server.user_token)
        .json(&serde_json::json!({ "requestId": request_id, "approve": true }))
        .send()
        .await
        .unwrap();
    assert_eq!(decision_res.status(), 200);
    let body: Value = decision_res.json().await.unwrap();
    body["redirectTo"].as_str().unwrap().to_string()
}

fn no_redirect_client() -> reqwest::Client {
    reqwest::Client::builder()
        .redirect(reqwest::redirect::Policy::none())
        .build()
        .unwrap()
}

/// Registers a client via the real admin endpoint (not the library function directly) — this
/// test's own way of also proving `POST /admin/oauth/clients` end to end.
async fn register_client(server: &TestServer, redirect_uri: &str, confidential: bool) -> (String, String) {
    let res = reqwest::Client::new()
        .post(format!("{}/admin/oauth/clients", server.base))
        .bearer_auth(&server.user_token)
        .json(&serde_json::json!({
            "name": "E2E Test Client",
            "redirectUris": [redirect_uri],
            "allowedScopes": ["read:widgets", "write:widgets"],
            "isConfidential": confidential,
        }))
        .send()
        .await
        .unwrap();
    assert_eq!(res.status(), 201);
    let body: Value = res.json().await.unwrap();
    let data = &body["data"];
    (
        data["clientId"].as_str().unwrap().to_string(),
        data["clientSecret"].as_str().unwrap().to_string(),
    )
}

/// Same as [`register_client`] but also returns the `serviceUserId` `POST /admin/oauth/clients`
/// eagerly provisions — only the `client_credentials` tests below need it (to grant that identity
/// a role and prove the resulting access token is governed by real RBAC, same as any other
/// session).
async fn register_client_with_service_user(
    server: &TestServer,
    redirect_uri: &str,
    confidential: bool,
) -> (String, String, Uuid) {
    let res = reqwest::Client::new()
        .post(format!("{}/admin/oauth/clients", server.base))
        .bearer_auth(&server.user_token)
        .json(&serde_json::json!({
            "name": "E2E Test Client",
            "redirectUris": [redirect_uri],
            "allowedScopes": ["read:widgets", "write:widgets"],
            "isConfidential": confidential,
        }))
        .send()
        .await
        .unwrap();
    assert_eq!(res.status(), 201);
    let body: Value = res.json().await.unwrap();
    let data = &body["data"];
    (
        data["clientId"].as_str().unwrap().to_string(),
        data["clientSecret"].as_str().unwrap().to_string(),
        Uuid::parse_str(data["serviceUserId"].as_str().unwrap()).unwrap(),
    )
}

fn extract_query_param(url: &str, key: &str) -> Option<String> {
    let query = url.split_once('?')?.1;
    query.split('&').find_map(|pair| {
        let (k, v) = pair.split_once('=')?;
        (k == key).then(|| urlencoding_decode(v))
    })
}

// Minimal decoder for the small, known character set this test's own values use (base64url +
// `%XX`) — not a general-purpose percent-decoder, just enough to read back what
// `routes::oauth2::percent_encode` produces.
fn urlencoding_decode(s: &str) -> String {
    let mut out = Vec::new();
    let bytes = s.as_bytes();
    let mut i = 0;
    while i < bytes.len() {
        if bytes[i] == b'%' && i + 2 < bytes.len() {
            let hex = std::str::from_utf8(&bytes[i + 1..i + 3]).unwrap();
            out.push(u8::from_str_radix(hex, 16).unwrap());
            i += 3;
        } else {
            out.push(bytes[i]);
            i += 1;
        }
    }
    String::from_utf8(out).unwrap()
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn full_authorization_code_and_refresh_lifecycle() {
    let tenant_id = Uuid::new_v4();
    let user_id = Uuid::new_v4();
    let server = boot_server(tenant_id, user_id).await;
    let redirect_uri = "https://example.com/callback";
    let (client_id, client_secret) = register_client(&server, redirect_uri, true).await;

    // 1. GET /oauth/authorize with the resource owner's own session -> a consent screen the first
    //    time (no prior grant for this client/user), then POST .../decision to approve it -> a
    //    redirect target carrying ?code=....
    let location = authorize_and_approve(&server, &client_id, redirect_uri, "read:widgets", "xyz123").await;
    assert!(location.starts_with(redirect_uri));
    assert_eq!(extract_query_param(&location, "state").as_deref(), Some("xyz123"));
    let code = extract_query_param(&location, "code").expect("redirect must carry a code");

    // 2. POST /oauth/token (authorization_code, HTTP Basic client auth) -> access + refresh token.
    let token_res = reqwest::Client::new()
        .post(format!("{}/oauth/token", server.base))
        .basic_auth(&client_id, Some(&client_secret))
        .form(&[
            ("grant_type", "authorization_code"),
            ("code", code.as_str()),
            ("redirect_uri", redirect_uri),
        ])
        .send()
        .await
        .unwrap();
    assert_eq!(token_res.status(), 200);
    let body: Value = token_res.json().await.unwrap();
    assert_eq!(body["token_type"], "Bearer");
    assert_eq!(body["scope"], "read:widgets");
    let access_token = body["access_token"].as_str().unwrap().to_string();
    let refresh_token = body["refresh_token"].as_str().unwrap().to_string();

    // 3. The minted access token is a real, verifiable platform JWT carrying the granted scope —
    // decode the JWT payload directly here (signature validity is `decode_access_token`'s own
    // concern, already covered by `jwt_security_postgres.rs`) to confirm the claim shape this
    // feature added.
    let payload_b64 = access_token.split('.').nth(1).unwrap();
    let payload_json = base64_url_decode(payload_b64);
    let claims: Value = serde_json::from_slice(&payload_json).unwrap();
    assert_eq!(claims["scope"], "read:widgets");
    assert_eq!(claims["clientId"], client_id);
    assert_eq!(claims["tenantId"], tenant_id.to_string());
    assert_eq!(claims["sub"], user_id.to_string());

    // 4. Replaying the same authorization code must now fail.
    let replay = reqwest::Client::new()
        .post(format!("{}/oauth/token", server.base))
        .basic_auth(&client_id, Some(&client_secret))
        .form(&[
            ("grant_type", "authorization_code"),
            ("code", code.as_str()),
            ("redirect_uri", redirect_uri),
        ])
        .send()
        .await
        .unwrap();
    assert_eq!(replay.status(), 400);
    let replay_body: Value = replay.json().await.unwrap();
    assert_eq!(replay_body["error"]["code"], "invalid_grant");

    // 5. refresh_token grant rotates and returns a fresh access token.
    let refresh_res = reqwest::Client::new()
        .post(format!("{}/oauth/token", server.base))
        .basic_auth(&client_id, Some(&client_secret))
        .form(&[
            ("grant_type", "refresh_token"),
            ("refresh_token", refresh_token.as_str()),
        ])
        .send()
        .await
        .unwrap();
    assert_eq!(refresh_res.status(), 200);
    let refresh_body: Value = refresh_res.json().await.unwrap();
    let new_access_token = refresh_body["access_token"].as_str().unwrap();
    let new_refresh_token = refresh_body["refresh_token"].as_str().unwrap();
    assert_ne!(new_access_token, access_token);
    assert_ne!(new_refresh_token, refresh_token);

    // 6. Replaying the pre-rotation refresh token must now fail (reuse detected).
    let old_refresh_replay = reqwest::Client::new()
        .post(format!("{}/oauth/token", server.base))
        .basic_auth(&client_id, Some(&client_secret))
        .form(&[
            ("grant_type", "refresh_token"),
            ("refresh_token", refresh_token.as_str()),
        ])
        .send()
        .await
        .unwrap();
    assert_eq!(old_refresh_replay.status(), 400);

    cleanup(&server.pool, tenant_id).await;
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn public_client_without_pkce_is_rejected() {
    let tenant_id = Uuid::new_v4();
    let user_id = Uuid::new_v4();
    let server = boot_server(tenant_id, user_id).await;
    let redirect_uri = "https://example.com/callback";
    let (client_id, _secret) = register_client(&server, redirect_uri, false).await;

    let res = no_redirect_client()
        .get(format!("{}/oauth/authorize", server.base))
        .bearer_auth(&server.user_token)
        .query(&[
            ("response_type", "code"),
            ("client_id", &client_id),
            ("redirect_uri", redirect_uri),
        ])
        .send()
        .await
        .unwrap();
    assert_eq!(res.status(), 400);
    let body: Value = res.json().await.unwrap();
    assert_eq!(body["error"]["code"], "invalid_request");

    cleanup(&server.pool, tenant_id).await;
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn wrong_client_secret_is_rejected() {
    let tenant_id = Uuid::new_v4();
    let user_id = Uuid::new_v4();
    let server = boot_server(tenant_id, user_id).await;
    let redirect_uri = "https://example.com/callback";
    let (client_id, _secret) = register_client(&server, redirect_uri, true).await;

    let res = reqwest::Client::new()
        .post(format!("{}/oauth/token", server.base))
        .basic_auth(&client_id, Some("definitely-wrong-secret"))
        .form(&[
            ("grant_type", "authorization_code"),
            ("code", "irrelevant"),
            ("redirect_uri", redirect_uri),
        ])
        .send()
        .await
        .unwrap();
    assert_eq!(res.status(), 401);
    let body: Value = res.json().await.unwrap();
    assert_eq!(body["error"]["code"], "invalid_client");

    cleanup(&server.pool, tenant_id).await;
}

fn base64_url_decode(s: &str) -> Vec<u8> {
    use base64::engine::general_purpose::URL_SAFE_NO_PAD;
    use base64::Engine as _;
    URL_SAFE_NO_PAD.decode(s).unwrap()
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn consent_screen_names_the_client_and_requested_scope() {
    let tenant_id = Uuid::new_v4();
    let user_id = Uuid::new_v4();
    let server = boot_server(tenant_id, user_id).await;
    let redirect_uri = "https://example.com/callback";
    let (client_id, _secret) = register_client(&server, redirect_uri, true).await;

    let res = no_redirect_client()
        .get(format!("{}/oauth/authorize", server.base))
        .bearer_auth(&server.user_token)
        .query(&[
            ("response_type", "code"),
            ("client_id", &client_id),
            ("redirect_uri", redirect_uri),
            ("scope", "read:widgets"),
        ])
        .send()
        .await
        .unwrap();
    assert_eq!(res.status(), 200);
    assert_eq!(
        res.headers().get("content-type").unwrap().to_str().unwrap(),
        "text/html; charset=utf-8"
    );
    let html = res.text().await.unwrap();
    assert!(html.contains("E2E Test Client"), "must show the client's own name");
    assert!(html.contains("read:widgets"), "must list the requested scope");
    // Nothing was decided yet — the pending row must still be there for a page reload.
    let request_id = extract_request_id(&html);
    assert!(metap_oauth_server::get_pending_authorization(&server.pool, request_id)
        .await
        .unwrap()
        .is_some());

    cleanup(&server.pool, tenant_id).await;
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn denying_consent_redirects_with_access_denied_and_no_code() {
    let tenant_id = Uuid::new_v4();
    let user_id = Uuid::new_v4();
    let server = boot_server(tenant_id, user_id).await;
    let redirect_uri = "https://example.com/callback";
    let (client_id, _secret) = register_client(&server, redirect_uri, true).await;

    let authorize_res = no_redirect_client()
        .get(format!("{}/oauth/authorize", server.base))
        .bearer_auth(&server.user_token)
        .query(&[
            ("response_type", "code"),
            ("client_id", &client_id),
            ("redirect_uri", redirect_uri),
            ("scope", "read:widgets"),
            ("state", "abc"),
        ])
        .send()
        .await
        .unwrap();
    let html = authorize_res.text().await.unwrap();
    let request_id = extract_request_id(&html);

    let decision_res = reqwest::Client::new()
        .post(format!("{}/oauth/authorize/decision", server.base))
        .bearer_auth(&server.user_token)
        .json(&serde_json::json!({ "requestId": request_id, "approve": false }))
        .send()
        .await
        .unwrap();
    assert_eq!(decision_res.status(), 200);
    let body: Value = decision_res.json().await.unwrap();
    let redirect_to = body["redirectTo"].as_str().unwrap();
    assert!(redirect_to.starts_with(redirect_uri));
    assert_eq!(
        extract_query_param(redirect_to, "error").as_deref(),
        Some("access_denied")
    );
    assert_eq!(extract_query_param(redirect_to, "state").as_deref(), Some("abc"));
    assert!(extract_query_param(redirect_to, "code").is_none());

    // Denying must not create a standing consent — a later request still needs a fresh screen.
    assert!(metap_oauth_server::get_consent_scope(
        &server.pool,
        metap_oauth_server::get_client_by_client_id(&server.pool, &client_id)
            .await
            .unwrap()
            .unwrap()
            .client
            .id,
        user_id
    )
    .await
    .unwrap()
    .is_none());

    // The pending row is consumed either way — re-deciding the same request must fail.
    let redecide = reqwest::Client::new()
        .post(format!("{}/oauth/authorize/decision", server.base))
        .bearer_auth(&server.user_token)
        .json(&serde_json::json!({ "requestId": request_id, "approve": true }))
        .send()
        .await
        .unwrap();
    assert_eq!(redecide.status(), 400);

    cleanup(&server.pool, tenant_id).await;
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn a_second_authorization_after_consent_skips_the_screen() {
    let tenant_id = Uuid::new_v4();
    let user_id = Uuid::new_v4();
    let server = boot_server(tenant_id, user_id).await;
    let redirect_uri = "https://example.com/callback";
    let (client_id, client_secret) = register_client(&server, redirect_uri, true).await;

    // First round trip: real consent screen, approved.
    let first_location = authorize_and_approve(&server, &client_id, redirect_uri, "read:widgets", "s1").await;
    let first_code = extract_query_param(&first_location, "code").unwrap();
    // Actually redeem it so its single-use guard can't be mistaken for what this test checks.
    reqwest::Client::new()
        .post(format!("{}/oauth/token", server.base))
        .basic_auth(&client_id, Some(&client_secret))
        .form(&[
            ("grant_type", "authorization_code"),
            ("code", first_code.as_str()),
            ("redirect_uri", redirect_uri),
        ])
        .send()
        .await
        .unwrap();

    // Second round trip, same client/user/scope: must go straight to a redirect, no screen.
    let second_res = no_redirect_client()
        .get(format!("{}/oauth/authorize", server.base))
        .bearer_auth(&server.user_token)
        .query(&[
            ("response_type", "code"),
            ("client_id", &client_id),
            ("redirect_uri", redirect_uri),
            ("scope", "read:widgets"),
            ("state", "s2"),
        ])
        .send()
        .await
        .unwrap();
    assert_eq!(
        second_res.status(),
        303,
        "a previously consented scope must skip the screen"
    );
    let location = second_res.headers().get("location").unwrap().to_str().unwrap();
    assert_eq!(extract_query_param(location, "state").as_deref(), Some("s2"));
    assert!(extract_query_param(location, "code").is_some());

    cleanup(&server.pool, tenant_id).await;
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn a_pending_authorization_cannot_be_decided_by_a_different_user_but_survives_for_the_real_owner() {
    let tenant_id = Uuid::new_v4();
    let user_id = Uuid::new_v4();
    let other_user_id = Uuid::new_v4();
    let server = boot_server(tenant_id, user_id).await;
    let redirect_uri = "https://example.com/callback";
    let (client_id, _secret) = register_client(&server, redirect_uri, true).await;

    let authorize_res = no_redirect_client()
        .get(format!("{}/oauth/authorize", server.base))
        .bearer_auth(&server.user_token)
        .query(&[
            ("response_type", "code"),
            ("client_id", &client_id),
            ("redirect_uri", redirect_uri),
            ("scope", "read:widgets"),
        ])
        .send()
        .await
        .unwrap();
    let html = authorize_res.text().await.unwrap();
    let request_id = extract_request_id(&html);

    let other_user_token = metap_peripherals::mint_jwt(&server.private_pem, tenant_id, other_user_id, 3600).unwrap();
    let res = reqwest::Client::new()
        .post(format!("{}/oauth/authorize/decision", server.base))
        .bearer_auth(&other_user_token)
        .json(&serde_json::json!({ "requestId": request_id, "approve": true }))
        .send()
        .await
        .unwrap();
    // Not a distinguishable 403 — a mismatched (tenant, user) comes back exactly like an
    // unknown/expired id (`consume_pending_authorization`'s own doc comment), so this also proves
    // the row was left alone for its real owner to still decide.
    assert_eq!(res.status(), 400);
    let body: Value = res.json().await.unwrap();
    assert_eq!(body["error"]["code"], "invalid_request");

    let decide_for_real = reqwest::Client::new()
        .post(format!("{}/oauth/authorize/decision", server.base))
        .bearer_auth(&server.user_token)
        .json(&serde_json::json!({ "requestId": request_id, "approve": true }))
        .send()
        .await
        .unwrap();
    assert_eq!(
        decide_for_real.status(),
        200,
        "the real owner must still be able to decide it afterwards"
    );

    cleanup(&server.pool, tenant_id).await;
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn client_credentials_mints_a_token_as_the_provisioned_service_user() {
    let tenant_id = Uuid::new_v4();
    let user_id = Uuid::new_v4();
    let server = boot_server(tenant_id, user_id).await;
    let redirect_uri = "https://example.com/callback";
    let (client_id, client_secret, service_user_id) =
        register_client_with_service_user(&server, redirect_uri, true).await;

    // No role granted yet — RBAC is deny-by-default for a brand-new identity, service accounts
    // included. Not exercised further here (a 403 from a business route is `metap-permission`'s
    // own concern, already covered elsewhere); this is just confirming the identity really is an
    // ordinary, unprivileged user until an admin grants it something.
    let has_no_roles: i64 = sqlx::query_scalar("SELECT count(*) FROM user_roles WHERE user_id = $1")
        .bind(service_user_id)
        .fetch_one(&server.pool)
        .await
        .unwrap();
    assert_eq!(has_no_roles, 0);

    let res = reqwest::Client::new()
        .post(format!("{}/oauth/token", server.base))
        .basic_auth(&client_id, Some(&client_secret))
        .form(&[("grant_type", "client_credentials"), ("scope", "read:widgets")])
        .send()
        .await
        .unwrap();
    assert_eq!(res.status(), 200);
    let body: Value = res.json().await.unwrap();
    assert_eq!(body["token_type"], "Bearer");
    assert_eq!(body["scope"], "read:widgets");
    assert!(
        body.get("refresh_token").is_none(),
        "RFC 6749 §4.4.3: no refresh token for client_credentials"
    );

    let access_token = body["access_token"].as_str().unwrap();
    let payload_json = base64_url_decode(access_token.split('.').nth(1).unwrap());
    let claims: Value = serde_json::from_slice(&payload_json).unwrap();
    assert_eq!(
        claims["sub"],
        service_user_id.to_string(),
        "token must be minted as the service user, not the admin who registered the client"
    );
    assert_eq!(claims["tenantId"], tenant_id.to_string());
    assert_eq!(claims["scope"], "read:widgets");
    assert_eq!(claims["clientId"], client_id);

    cleanup(&server.pool, tenant_id).await;
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn client_credentials_is_rejected_for_a_public_client() {
    let tenant_id = Uuid::new_v4();
    let user_id = Uuid::new_v4();
    let server = boot_server(tenant_id, user_id).await;
    let redirect_uri = "https://example.com/callback";
    let (client_id, client_secret, _service_user_id) =
        register_client_with_service_user(&server, redirect_uri, false).await;

    let res = reqwest::Client::new()
        .post(format!("{}/oauth/token", server.base))
        .basic_auth(&client_id, Some(&client_secret))
        .form(&[("grant_type", "client_credentials")])
        .send()
        .await
        .unwrap();
    assert_eq!(res.status(), 400);
    let body: Value = res.json().await.unwrap();
    assert_eq!(body["error"]["code"], "unauthorized_client");

    cleanup(&server.pool, tenant_id).await;
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn client_credentials_rejects_a_scope_the_client_is_not_allowed() {
    let tenant_id = Uuid::new_v4();
    let user_id = Uuid::new_v4();
    let server = boot_server(tenant_id, user_id).await;
    let redirect_uri = "https://example.com/callback";
    let (client_id, client_secret, _service_user_id) =
        register_client_with_service_user(&server, redirect_uri, true).await;

    let res = reqwest::Client::new()
        .post(format!("{}/oauth/token", server.base))
        .basic_auth(&client_id, Some(&client_secret))
        .form(&[("grant_type", "client_credentials"), ("scope", "delete:everything")])
        .send()
        .await
        .unwrap();
    assert_eq!(res.status(), 400);
    let body: Value = res.json().await.unwrap();
    assert_eq!(body["error"]["code"], "invalid_scope");

    cleanup(&server.pool, tenant_id).await;
}
