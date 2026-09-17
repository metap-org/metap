//! Live-verifies the plain-OAuth2 login provider's wire protocol (code exchange -> userinfo call
//! -> identity) against a mocked IdP — mirrors `oidc_e2e.rs`'s own structure and reasoning for
//! why a mock stands in for a real external provider in this environment. The wire-protocol tests
//! (no `#[ignore]`) touch no Postgres; the 2 JIT-provisioning tests at the bottom do
//! (`#[ignore]`d, same convention as `oidc_e2e.rs`'s own `jit_provisioning_*` test).

use serde_json::json;
use sqlx::PgPool;
use uuid::Uuid;
use wiremock::matchers::{header, method, path};
use wiremock::{Mock, MockServer, ResponseTemplate};

use metap_auth::{oauth2_login_authorize_url, oauth2_login_verify_callback, OAuth2LoginConfig};

fn test_config(idp: &MockServer) -> OAuth2LoginConfig {
    OAuth2LoginConfig {
        authorize_url: format!("{}/authorize", idp.uri()),
        token_url: format!("{}/token", idp.uri()),
        userinfo_url: format!("{}/user", idp.uri()),
        client_id: "test-client".to_string(),
        client_secret_ref: "TEST_OAUTH2_CLIENT_SECRET".to_string(),
        redirect_uri: "http://localhost:3000/auth/oauth2/00000000-0000-0000-0000-000000000000/callback".to_string(),
        scopes: vec!["read:user".to_string()],
        post_login_redirect: "http://localhost:5173/auth/oauth2/callback".to_string(),
        subject_field: "id".to_string(),
        email_field: "email".to_string(),
    }
}

async fn mock_token_and_userinfo(idp: &MockServer, subject: impl serde::Serialize, email: &str) {
    Mock::given(method("POST"))
        .and(path("/token"))
        .respond_with(ResponseTemplate::new(200).set_body_json(json!({
            "access_token": "test-access-token",
            "token_type": "bearer",
            "expires_in": 3600,
        })))
        .mount(idp)
        .await;

    Mock::given(method("GET"))
        .and(path("/user"))
        .and(header("authorization", "Bearer test-access-token"))
        .respond_with(ResponseTemplate::new(200).set_body_json(json!({
            "id": subject,
            "email": email,
            "name": "Ignored Field",
        })))
        .mount(idp)
        .await;
}

/// The full client-side protocol this crate drives, against a mocked IdP — code exchange, then
/// an authenticated userinfo call, exactly as GitHub's own OAuth app flow works (no id_token,
/// unlike OIDC — this is the whole reason this provider exists as a separate code path).
#[tokio::test]
async fn authorize_and_verify_callback_recovers_the_idp_identity_via_userinfo() {
    let idp = MockServer::start().await;
    let config = test_config(&idp);
    // GitHub's own `id` field is numeric — chosen deliberately here to exercise the "subject
    // field may be a JSON number" normalization `oauth2_login_verify_callback` does.
    mock_token_and_userinfo(&idp, 987654321u64, "person@example.com").await;

    let (auth_url, _csrf_token, pkce_verifier) = oauth2_login_authorize_url(&config, "shhh").unwrap();
    assert!(auth_url.starts_with(&config.authorize_url));

    let identity = oauth2_login_verify_callback(&config, "shhh", "any-code", &pkce_verifier)
        .await
        .unwrap();
    assert_eq!(identity.email, "person@example.com");
    assert_eq!(identity.external_subject, "987654321");
}

/// A provider whose `id` field is a string (matching OIDC's own `sub` convention, e.g. many
/// non-GitHub OAuth2 IdPs) must work identically — the normalization must not assume a number.
#[tokio::test]
async fn string_shaped_subject_field_also_works() {
    let idp = MockServer::start().await;
    let config = test_config(&idp);
    mock_token_and_userinfo(&idp, "sub-string-42", "another@example.com").await;

    let (_url, _csrf_token, pkce_verifier) = oauth2_login_authorize_url(&config, "shhh").unwrap();
    let identity = oauth2_login_verify_callback(&config, "shhh", "any-code", &pkce_verifier)
        .await
        .unwrap();
    assert_eq!(identity.external_subject, "sub-string-42");
}

/// A configured `subject_field`/`email_field` that doesn't match a provider's real response shape
/// must fail loudly, not silently fall back to some default — a misconfigured tenant should see
/// an error at login time, not a JIT-provisioned user with a garbage identity.
#[tokio::test]
async fn missing_configured_field_in_the_userinfo_response_is_an_error() {
    let idp = MockServer::start().await;
    let mut config = test_config(&idp);
    config.subject_field = "not_a_real_field".to_string();
    mock_token_and_userinfo(&idp, "some-subject", "person@example.com").await;

    let (_url, _csrf_token, pkce_verifier) = oauth2_login_authorize_url(&config, "shhh").unwrap();
    let result = oauth2_login_verify_callback(&config, "shhh", "any-code", &pkce_verifier).await;
    assert!(result.is_err());
}

/// Default field names (`"id"`/`"email"`) apply when a tenant's config doesn't set them —
/// `#[serde(default)]` on `OAuth2LoginConfig` deserialized from a minimal JSON payload, the shape
/// `tenant_auth_configs.config` actually round-trips through.
#[test]
fn config_defaults_subject_and_email_field_names_when_omitted() {
    let minimal = json!({
        "authorize_url": "https://idp.example/authorize",
        "token_url": "https://idp.example/token",
        "userinfo_url": "https://idp.example/user",
        "client_id": "c",
        "client_secret_ref": "REF",
        "redirect_uri": "https://app.example/callback",
        "post_login_redirect": "https://app.example/",
    });
    let config: OAuth2LoginConfig = serde_json::from_value(minimal).unwrap();
    assert_eq!(config.subject_field, "id");
    assert_eq!(config.email_field, "email");
    assert!(config.scopes.is_empty());
}

async fn connect() -> PgPool {
    let database_url = std::env::var("DATABASE_URL").expect("DATABASE_URL required for this e2e test");
    sqlx::postgres::PgPoolOptions::new()
        .max_connections(3)
        .connect(&database_url)
        .await
        .unwrap()
}

async fn cleanup(pool: &PgPool, tenant_id: Uuid) {
    sqlx::query("DELETE FROM users WHERE tenant_id = $1")
        .bind(tenant_id)
        .execute(pool)
        .await
        .ok();
}

/// Same JIT-provisioning guarantee `oidc_e2e.rs`'s own test proves for `"oidc"`, run here against
/// the generalized `find_external_user`/`jit_provision_external_user` with `"oauth2"` — confirms
/// the generalization (this crate's `lib.rs`) didn't change behavior for either provider.
#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn jit_provisioning_creates_once_then_links_on_repeat_login() {
    let pool = connect().await;
    let tenant_id = Uuid::new_v4();

    let none_yet = metap_auth::find_external_user(&pool, tenant_id, "oauth2", "repeat-subject-1")
        .await
        .unwrap();
    assert!(none_yet.is_none());

    let created = metap_auth::jit_provision_external_user(
        &pool,
        tenant_id,
        "oauth2",
        "first-login@example.com",
        "repeat-subject-1",
    )
    .await
    .unwrap();
    assert_eq!(created.email, "first-login@example.com");

    let found_again = metap_auth::find_external_user(&pool, tenant_id, "oauth2", "repeat-subject-1")
        .await
        .unwrap()
        .expect("second login must find the row JIT-provisioned by the first");
    assert_eq!(
        found_again.id, created.id,
        "must link to the same user, not create a duplicate"
    );

    cleanup(&pool, tenant_id).await;
}

/// The same `external_subject` string under two different providers must resolve to two
/// different users, never collide — the reason `users_tenant_external_subject_idx`
/// (`0020_users_oidc_columns.sql`) is keyed on `(tenant_id, auth_provider, external_subject)`,
/// not just `(tenant_id, external_subject)`.
#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn same_external_subject_under_different_providers_does_not_collide() {
    let pool = connect().await;
    let tenant_id = Uuid::new_v4();

    let oidc_user =
        metap_auth::jit_provision_external_user(&pool, tenant_id, "oidc", "a@example.com", "shared-subject")
            .await
            .unwrap();
    let oauth2_user =
        metap_auth::jit_provision_external_user(&pool, tenant_id, "oauth2", "b@example.com", "shared-subject")
            .await
            .unwrap();
    assert_ne!(oidc_user.id, oauth2_user.id);

    let found_oidc = metap_auth::find_external_user(&pool, tenant_id, "oidc", "shared-subject")
        .await
        .unwrap()
        .unwrap();
    let found_oauth2 = metap_auth::find_external_user(&pool, tenant_id, "oauth2", "shared-subject")
        .await
        .unwrap()
        .unwrap();
    assert_eq!(found_oidc.id, oidc_user.id);
    assert_eq!(found_oauth2.id, oauth2_user.id);

    cleanup(&pool, tenant_id).await;
}
