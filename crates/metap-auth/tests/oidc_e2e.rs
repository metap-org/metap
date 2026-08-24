//! Live-verifies the actual `openidconnect` wire protocol this crate drives (discovery → JWKS →
//! authorize-url generation → code exchange → id_token signature+nonce verification), against a
//! `wiremock`-mocked IdP instead of a real external provider (nothing in this dev environment can
//! reach one). A hand-signed RS256 id_token (via `jsonwebtoken` + a freshly generated `rsa`
//! keypair, both already dependencies elsewhere in this repo — no new crypto surface trusted)
//! stands in for what a real IdP would return. `jit_provisioning_...` also touches real Postgres
//! (`#[ignore]`d, same convention as every other e2e test in this repo).

use std::time::{SystemTime, UNIX_EPOCH};

use base64::engine::general_purpose::URL_SAFE_NO_PAD;
use base64::Engine as _;
use rsa::pkcs1::EncodeRsaPrivateKey;
use rsa::traits::PublicKeyParts;
use rsa::{RsaPrivateKey, RsaPublicKey};
use serde_json::json;
use sqlx::PgPool;
use uuid::Uuid;
use wiremock::matchers::{method, path};
use wiremock::{Mock, MockServer, ResponseTemplate};

use metap_auth::{oidc_authorize_url, oidc_verify_callback, OidcConfig};

struct TestIdp {
    server: MockServer,
    encoding_key: jsonwebtoken::EncodingKey,
    kid: String,
}

impl TestIdp {
    /// Spins up a mock IdP serving discovery + JWKS, both fixed for the server's lifetime — the
    /// per-test mock only needs to vary the `/token` response (the id_token's claims), added by
    /// `respond_with_id_token`.
    async fn start() -> Self {
        let mut rng = rand::thread_rng();
        let private_key = RsaPrivateKey::new(&mut rng, 2048).expect("RSA keygen must not fail in a test");
        let public_key = RsaPublicKey::from(&private_key);
        let pem = private_key
            .to_pkcs1_pem(rsa::pkcs1::LineEnding::LF)
            .expect("PEM export");
        let encoding_key = jsonwebtoken::EncodingKey::from_rsa_pem(pem.as_bytes()).expect("valid RSA PEM");

        let kid = "test-key".to_string();
        let n = URL_SAFE_NO_PAD.encode(public_key.n().to_bytes_be());
        let e = URL_SAFE_NO_PAD.encode(public_key.e().to_bytes_be());

        let server = MockServer::start().await;
        let issuer = server.uri();

        Mock::given(method("GET"))
            .and(path("/.well-known/openid-configuration"))
            .respond_with(ResponseTemplate::new(200).set_body_json(json!({
                "issuer": issuer,
                "authorization_endpoint": format!("{issuer}/authorize"),
                "token_endpoint": format!("{issuer}/token"),
                "jwks_uri": format!("{issuer}/jwks"),
                "response_types_supported": ["code"],
                "subject_types_supported": ["public"],
                "id_token_signing_alg_values_supported": ["RS256"],
            })))
            .mount(&server)
            .await;

        Mock::given(method("GET"))
            .and(path("/jwks"))
            .respond_with(ResponseTemplate::new(200).set_body_json(json!({
                "keys": [{"kty": "RSA", "use": "sig", "alg": "RS256", "kid": kid, "n": n, "e": e}]
            })))
            .mount(&server)
            .await;

        Self {
            server,
            encoding_key,
            kid,
        }
    }

    fn issuer_url(&self) -> String {
        self.server.uri()
    }

    /// Signs and registers the id_token the mocked `/token` endpoint will return on the next
    /// exchange — `nonce` must be the exact value `oidc_authorize_url` generated, exactly as a
    /// real callback's id_token must echo back the nonce the client sent at `/authorize`.
    async fn respond_with_id_token(&self, client_id: &str, subject: &str, email: &str, nonce: &str) {
        let now = SystemTime::now().duration_since(UNIX_EPOCH).unwrap().as_secs();
        let claims = json!({
            "iss": self.issuer_url(),
            "sub": subject,
            "aud": client_id,
            "exp": now + 300,
            "iat": now,
            "nonce": nonce,
            "email": email,
        });
        let mut header = jsonwebtoken::Header::new(jsonwebtoken::Algorithm::RS256);
        header.kid = Some(self.kid.clone());
        let id_token = jsonwebtoken::encode(&header, &claims, &self.encoding_key).expect("sign id_token");

        Mock::given(method("POST"))
            .and(path("/token"))
            .respond_with(ResponseTemplate::new(200).set_body_json(json!({
                "access_token": "test-access-token",
                "token_type": "Bearer",
                "id_token": id_token,
                "expires_in": 300,
            })))
            .mount(&self.server)
            .await;
    }
}

fn test_config(idp: &TestIdp) -> OidcConfig {
    OidcConfig {
        issuer_url: idp.issuer_url(),
        client_id: "test-client".to_string(),
        client_secret_ref: "TEST_OIDC_CLIENT_SECRET".to_string(),
        redirect_uri: "http://localhost:3000/auth/oidc/00000000-0000-0000-0000-000000000000/callback".to_string(),
        scopes: vec!["openid".to_string(), "email".to_string()],
        post_login_redirect: "http://localhost:5173/auth/oidc/callback".to_string(),
    }
}

/// The full client-side protocol this crate drives, against a real (mocked) IdP — not just
/// isolated function calls: `oidc_authorize_url`'s generated nonce must be exactly what
/// `oidc_verify_callback` receives back for the whole thing to succeed, same as a real redirect
/// round-trip.
#[tokio::test]
async fn authorize_and_verify_callback_round_trip_recovers_the_idp_identity() {
    let idp = TestIdp::start().await;
    let config = test_config(&idp);

    let (auth_url, _csrf_token, nonce, pkce_verifier) = oidc_authorize_url(&config, "shhh").await.unwrap();
    assert!(auth_url.starts_with(&format!("{}/authorize", idp.issuer_url())));

    idp.respond_with_id_token(&config.client_id, "idp-subject-42", "person@example.com", &nonce)
        .await;

    let identity = oidc_verify_callback(&config, "shhh", "any-code", &nonce, &pkce_verifier)
        .await
        .unwrap();
    assert_eq!(identity.email, "person@example.com");
    assert_eq!(identity.external_subject, "idp-subject-42");
}

/// A tampered/mismatched nonce (someone replaying an old id_token, or a CSRF-state mixup) must
/// be rejected — `openidconnect`'s own nonce check, not something this crate re-implements, but
/// worth confirming it's actually wired through rather than silently skipped.
#[tokio::test]
async fn wrong_nonce_is_rejected() {
    let idp = TestIdp::start().await;
    let config = test_config(&idp);

    let (_url, _csrf_token, _nonce, pkce_verifier) = oidc_authorize_url(&config, "shhh").await.unwrap();
    idp.respond_with_id_token(
        &config.client_id,
        "idp-subject-42",
        "person@example.com",
        "correct-nonce",
    )
    .await;

    let result = oidc_verify_callback(&config, "shhh", "any-code", "wrong-nonce", &pkce_verifier).await;
    assert!(
        result.is_err(),
        "id_token with a mismatched nonce must fail verification"
    );
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

/// The JIT-provisioning decision (project owner, 2026-08-24): first OIDC login for an
/// `external_subject` creates a `users` row; a second login with the same `external_subject`
/// must find and reuse that exact row, never create a duplicate — the whole reason lookup is by
/// `external_subject`, not email.
#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn jit_provisioning_creates_once_then_links_on_repeat_login() {
    let pool = connect().await;
    let tenant_id = Uuid::new_v4();

    let none_yet = metap_auth::find_oidc_user(&pool, tenant_id, "repeat-subject-1")
        .await
        .unwrap();
    assert!(none_yet.is_none());

    let created = metap_auth::jit_provision_oidc_user(&pool, tenant_id, "first-login@example.com", "repeat-subject-1")
        .await
        .unwrap();
    assert_eq!(created.email, "first-login@example.com");

    let found_again = metap_auth::find_oidc_user(&pool, tenant_id, "repeat-subject-1")
        .await
        .unwrap()
        .expect("second login must find the row JIT-provisioned by the first");
    assert_eq!(
        found_again.id, created.id,
        "must link to the same user, not create a duplicate"
    );

    cleanup(&pool, tenant_id).await;
}
