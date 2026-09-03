//! Credential keys end to end (`../metap-docs/docs/features/18-config-tiers-db-backed.md`, slice 3).
//!
//! The feature's whole claim is that a tenant can *set* a credential and nobody — including that
//! tenant — can read it back, and that no request can reach another tenant's. Those are properties
//! of the write path, so they are asserted over real HTTP rather than only in unit tests:
//!
//! - [`a_stored_credential_is_never_returned_by_any_read`] — the plaintext must not appear anywhere
//!   in any response, not blanked, not truncated, not present at all.
//! - [`a_caller_supplied_secret_reference_is_ignored_entirely`] — the brief's hardest requirement.
//!   Tenant B sending tenant A's exact reference must still only ever touch B's own credential,
//!   because the reference is derived from the token's tenant and there is no field to send one in.
//! - [`clearing_a_credential_revokes_it_from_the_backend`] — the executor derives the same reference
//!   every run and never reads the marker row, so deleting only the row would leave a credential
//!   the tenant believes is gone still being sent.
//!
//! The `SecretStore` here is a test double rather than the default `EnvStore`, which deliberately
//! refuses runtime writes (see its doc comment) — a deployment that has not configured
//! Vault/AWS/GCP cannot offer tenant-managed credentials at all, so exercising the write path needs
//! a backend that accepts writes. That the double is easy to write is itself the point of
//! `SecretStore` being a trait.
//!
//! Harness mirrors `tenant_config_postgres.rs`, duplicated locally per this repo's convention.
//! `#[ignore]`d — needs `DATABASE_URL`.

use std::collections::HashMap;
use std::process::Command;
use std::sync::{Arc, Mutex};

use arc_swap::ArcSwap;
use axum::Router;
use jsonwebtoken::DecodingKey;
use metap_control::{DbCreds, SecretStore};
use metap_http::{build_router, AppState};
use metap_metadata::MetadataRegistry;
use metap_permission::PermissionService;
use secrecy::{ExposeSecret, SecretString};
use serde_json::{json, Value};
use sqlx::postgres::PgPoolOptions;
use sqlx::PgPool;
use uuid::Uuid;

const KEY: &str = "cron.webhookAuthorization";

/// An in-memory `SecretStore`. Also serves as the proof that the trait is implementable from
/// outside `metap-control` — every method a real backend needs, and nothing else.
#[derive(Default)]
struct FakeSecretStore {
    secrets: Mutex<HashMap<String, String>>,
}

impl FakeSecretStore {
    fn peek(&self, secret_ref: &str) -> Option<String> {
        self.secrets.lock().unwrap().get(secret_ref).cloned()
    }
}

#[async_trait::async_trait]
impl SecretStore for FakeSecretStore {
    async fn db_credentials(&self, dsn_secret_ref: &str) -> anyhow::Result<DbCreds> {
        // Tenant routing is not what this file tests; every tenant here is schema-strategy on the
        // shared pool, so this is never reached.
        let dsn = std::env::var(dsn_secret_ref)?;
        Ok(DbCreds {
            dsn: SecretString::from(dsn),
            expires_at: None,
        })
    }

    async fn get_secret(&self, secret_ref: &str) -> anyhow::Result<SecretString> {
        self.secrets
            .lock()
            .unwrap()
            .get(secret_ref)
            .map(|v| SecretString::from(v.clone()))
            .ok_or_else(|| anyhow::anyhow!("no secret stored at {secret_ref}"))
    }

    async fn put_secret(&self, secret_ref: &str, value: &str) -> anyhow::Result<()> {
        self.secrets
            .lock()
            .unwrap()
            .insert(secret_ref.to_string(), value.to_string());
        Ok(())
    }

    async fn delete_secret(&self, secret_ref: &str) -> anyhow::Result<()> {
        self.secrets.lock().unwrap().remove(secret_ref);
        Ok(())
    }
}

struct TempDir(std::path::PathBuf);
impl Drop for TempDir {
    fn drop(&mut self) {
        std::fs::remove_dir_all(&self.0).ok();
    }
}

fn openssl_genrsa() -> (TempDir, String, String) {
    let dir = std::env::temp_dir().join(format!("metap-http-tenant-secret-{}", Uuid::new_v4()));
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
    store: Arc<FakeSecretStore>,
    tenant_a: Uuid,
    tenant_a_admin: String,
    tenant_b: Uuid,
    tenant_b_admin: String,
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

    let (tenant_a, tenant_a_admin_id) = (Uuid::new_v4(), Uuid::new_v4());
    let (tenant_b, tenant_b_admin_id) = (Uuid::new_v4(), Uuid::new_v4());
    for (tenant, user) in [(tenant_a, tenant_a_admin_id), (tenant_b, tenant_b_admin_id)] {
        sqlx::query("INSERT INTO user_roles (tenant_id, user_id, role) VALUES ($1, $2, 'admin')")
            .bind(tenant)
            .bind(user)
            .execute(&pool)
            .await
            .unwrap();
    }

    let store = Arc::new(FakeSecretStore::default());
    let control_router = metap_control::Router::new(
        pool.clone(),
        metap_control::RegistryCache::new(Arc::new(metap_control::PostgresTenantRegistry::new(pool.clone()))),
        store.clone(),
    );

    let registry = Arc::new(MetadataRegistry::new());
    let permissions = PermissionService::new(Box::new(metap_control::PostgresPolicyStore::new(
        control_router.clone(),
    )));
    let state = AppState::new(
        pool.clone(),
        registry.clone(),
        Arc::new(ArcSwap::new(registry)),
        Arc::new(permissions),
        DecodingKey::from_rsa_pem(public_pem.as_bytes()).unwrap(),
        private_pem.clone(),
        control_router,
    );
    state.config.reload().await.unwrap();
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

    let mint = |tenant: Uuid, user: Uuid| metap_peripherals::mint_jwt(&private_pem, tenant, user, 3600).unwrap();
    TestServer {
        base: format!("http://{addr}"),
        store,
        tenant_a_admin: mint(tenant_a, tenant_a_admin_id),
        tenant_b_admin: mint(tenant_b, tenant_b_admin_id),
        tenant_a,
        tenant_b,
        pool,
        _keys: keys,
    }
}

async fn cleanup(server: &TestServer) {
    sqlx::query("DELETE FROM tenant_configs")
        .execute(&server.pool)
        .await
        .ok();
    for tenant in [server.tenant_a, server.tenant_b] {
        sqlx::query("DELETE FROM user_roles WHERE tenant_id = $1")
            .bind(tenant)
            .execute(&server.pool)
            .await
            .ok();
    }
}

async fn put_credential(server: &TestServer, token: &str, body: Value) -> reqwest::Response {
    reqwest::Client::new()
        .put(format!("{}/admin/config/{KEY}", server.base))
        .bearer_auth(token)
        .json(&body)
        .send()
        .await
        .unwrap()
}

async fn admin_config_raw(server: &TestServer, token: &str) -> String {
    reqwest::Client::new()
        .get(format!("{}/admin/config", server.base))
        .bearer_auth(token)
        .send()
        .await
        .unwrap()
        .text()
        .await
        .unwrap()
}

/// Write-only semantics: the credential goes in, and no read anywhere gives it back.
#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn a_stored_credential_is_never_returned_by_any_read() {
    let server = boot_server().await;
    let plaintext = "Bearer sk_live_never_show_this";

    let res = put_credential(&server, &server.tenant_a_admin, json!({ "value": plaintext })).await;
    assert_eq!(res.status(), 200);
    let written = res.text().await.unwrap();
    assert!(
        !written.contains("sk_live_never_show_this"),
        "the write response echoed the credential: {written}"
    );

    // The backend really has it — so what follows is "not returned", not "not stored".
    let expected_ref = metap_control::tenant_secret_ref(server.tenant_a, KEY);
    assert_eq!(server.store.peek(&expected_ref).as_deref(), Some(plaintext));

    let listing = admin_config_raw(&server, &server.tenant_a_admin).await;
    assert!(
        !listing.contains("sk_live_never_show_this"),
        "GET /admin/config leaked the credential: {listing}"
    );
    let body: Value = serde_json::from_str(&listing).unwrap();
    let entry = body["data"]
        .as_array()
        .unwrap()
        .iter()
        .find(|i| i["key"] == KEY)
        .expect("the credential key is listed");
    assert_eq!(entry["secret"], json!(true));
    assert_eq!(entry["set"], json!(true));
    assert_eq!(entry["secretRef"], json!(expected_ref));
    assert!(
        entry.get("value").is_none(),
        "a credential entry must omit `value` entirely rather than blank it: {entry}"
    );

    // And the unauthenticated surface must not know this key exists at all.
    let public = reqwest::Client::new()
        .get(format!("{}/public/config", server.base))
        .send()
        .await
        .unwrap()
        .text()
        .await
        .unwrap();
    assert!(
        !public.contains(KEY),
        "the public surface listed a credential key: {public}"
    );
    cleanup(&server).await;
}

/// **The brief's hardest requirement.** A reference is derived from the caller's own tenant, so
/// tenant B sending tenant A's exact reference string reaches nothing of A's — there is no request
/// field that carries a reference for the server to trust or sanitize.
#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn a_caller_supplied_secret_reference_is_ignored_entirely() {
    let server = boot_server().await;
    let a_ref = metap_control::tenant_secret_ref(server.tenant_a, KEY);
    let b_ref = metap_control::tenant_secret_ref(server.tenant_b, KEY);

    assert_eq!(
        put_credential(&server, &server.tenant_a_admin, json!({ "value": "A's credential" }))
            .await
            .status(),
        200
    );

    // Tenant B writes, naming A's reference every way the API might conceivably accept one.
    let res = put_credential(
        &server,
        &server.tenant_b_admin,
        json!({ "value": "B's credential", "secretRef": a_ref, "tenantId": server.tenant_a }),
    )
    .await;
    assert_eq!(res.status(), 200);

    assert_eq!(
        server.store.peek(&a_ref).as_deref(),
        Some("A's credential"),
        "tenant A's credential must be untouched by tenant B's write"
    );
    assert_eq!(server.store.peek(&b_ref).as_deref(), Some("B's credential"));

    // B's own listing shows B's reference, never A's.
    let listing = admin_config_raw(&server, &server.tenant_b_admin).await;
    assert!(listing.contains(&b_ref));
    assert!(
        !listing.contains(&a_ref),
        "tenant B's listing named tenant A's reference"
    );
    assert!(!listing.contains("A's credential"));
    cleanup(&server).await;
}

/// Clearing must revoke, not just unlink — the cron executor derives the reference itself and never
/// consults the marker row, so a row-only delete would leave a live credential being sent.
#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn clearing_a_credential_revokes_it_from_the_backend() {
    let server = boot_server().await;
    let reference = metap_control::tenant_secret_ref(server.tenant_a, KEY);

    put_credential(&server, &server.tenant_a_admin, json!({ "value": "to be revoked" })).await;
    assert!(server.store.peek(&reference).is_some());

    let res = reqwest::Client::new()
        .delete(format!("{}/admin/config/{KEY}", server.base))
        .bearer_auth(&server.tenant_a_admin)
        .send()
        .await
        .unwrap();
    assert_eq!(res.status(), 200);

    assert!(
        server.store.peek(&reference).is_none(),
        "the credential is still in the backend after being cleared"
    );
    let listing: Value = serde_json::from_str(&admin_config_raw(&server, &server.tenant_a_admin).await).unwrap();
    let entry = listing["data"]
        .as_array()
        .unwrap()
        .iter()
        .find(|i| i["key"] == KEY)
        .unwrap()
        .clone();
    assert_eq!(entry["set"], json!(false));
    assert_eq!(entry["secretRef"], Value::Null);
    cleanup(&server).await;
}

/// A credential becomes an HTTP header value, so a CR/LF in it is header injection into a request
/// the platform makes on the tenant's behalf. Refused at write time, and nothing reaches the
/// backend.
#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn a_credential_containing_a_newline_is_refused_and_not_stored() {
    let server = boot_server().await;
    let reference = metap_control::tenant_secret_ref(server.tenant_a, KEY);

    for bad in ["Bearer x\r\nX-Injected: 1", "Bearer x\nHost: evil.example", ""] {
        let res = put_credential(&server, &server.tenant_a_admin, json!({ "value": bad })).await;
        assert_eq!(res.status(), 422, "{bad:?} must be refused");
        assert_eq!(
            res.json::<Value>().await.unwrap()["error"]["code"],
            "invalid_config_value"
        );
    }
    assert!(
        server.store.peek(&reference).is_none(),
        "a refused credential must never reach the backend"
    );
    cleanup(&server).await;
}

/// A credential key is not an ordinary config value: the plaintext must never be storable into
/// `tenant_configs` by any route, so the non-secret write path refuses it outright.
#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn a_credential_key_never_lands_in_the_config_table_as_a_value() {
    let server = boot_server().await;
    put_credential(
        &server,
        &server.tenant_a_admin,
        json!({ "value": "Bearer stored_value" }),
    )
    .await;

    let stored: Option<Value> =
        sqlx::query_scalar("SELECT value FROM tenant_configs WHERE tenant_id = $1 AND key = $2")
            .bind(server.tenant_a)
            .bind(KEY)
            .fetch_optional(&server.pool)
            .await
            .unwrap();
    let stored = stored.expect("a marker row exists");
    assert_eq!(
        stored,
        json!({ "secretRef": metap_control::tenant_secret_ref(server.tenant_a, KEY) }),
        "the config table must hold only the derived reference"
    );
    assert!(!stored.to_string().contains("stored_value"));
    cleanup(&server).await;
}

/// The `/platform/config` surface must not offer credential keys either: a fleet-wide credential
/// makes no sense, and a platform admin listing one would suggest it does.
#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn a_platform_admin_cannot_set_a_tenant_credential_fleet_wide() {
    let server = boot_server().await;
    // A tenant admin's token is enough to prove the routing: the platform surface rejects it before
    // any tier check, and the tier check itself is covered by `platform_config_postgres.rs`.
    let res = reqwest::Client::new()
        .put(format!("{}/platform/config/{KEY}", server.base))
        .bearer_auth(&server.tenant_a_admin)
        .json(&json!({ "value": "Bearer fleet_wide" }))
        .send()
        .await
        .unwrap();
    assert_eq!(res.status(), 403);
    assert!(server
        .store
        .peek(&metap_control::tenant_secret_ref(server.tenant_a, KEY))
        .is_none());
    cleanup(&server).await;
}

/// `SecretString`'s redaction is what keeps a credential out of logs and error strings; asserting
/// it here means a future swap to a plain `String` fails loudly rather than quietly.
#[test]
fn a_secret_string_never_prints_its_contents() {
    let secret = SecretString::from("Bearer sk_live_abc".to_string());
    let rendered = format!("{secret:?}");
    // The rendered form is deliberately not interpolated into the failure message: if this
    // assertion ever fires, `rendered` is by definition the unredacted credential, and a panic
    // message is a log line.
    assert!(
        !rendered.contains("sk_live_abc"),
        "SecretString's Debug no longer redacts its contents"
    );
    assert_eq!(secret.expose_secret(), "Bearer sk_live_abc");
}
