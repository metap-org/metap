//! `/platform/config` end to end (`../metap-docs/docs/features/18-config-tiers-db-backed.md`,
//! slice 1).
//!
//! The test that matters most here is [`an_operator_key_is_refused_even_for_a_platform_admin`]:
//! the SSRF guard `cron-scheduler` gained for audit 04 A#1 is only worth anything because its
//! allowlist is operator-controlled, so a convenient config API that could write those keys would
//! silently undo that fix. That boundary is asserted over real HTTP rather than only in
//! `metap_config::keys`'s unit tests, because it is the *route* that a future change would most
//! plausibly loosen.
//!
//! Harness mirrors `http_server.rs`/`jwt_security_postgres.rs`, duplicated locally per this repo's
//! convention. `#[ignore]`d — needs `DATABASE_URL`.

use std::process::Command;
use std::sync::Arc;

use arc_swap::ArcSwap;
use axum::Router;
use jsonwebtoken::DecodingKey;
use metap_http::{build_router, AppState};
use metap_metadata::MetadataRegistry;
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

struct TempDir(std::path::PathBuf);
impl TempDir {
    fn new() -> Self {
        let path = std::env::temp_dir().join(format!("metap-http-config-test-{}", Uuid::new_v4()));
        std::fs::create_dir_all(&path).unwrap();
        Self(path)
    }
}
impl Drop for TempDir {
    fn drop(&mut self) {
        std::fs::remove_dir_all(&self.0).ok();
    }
}

fn openssl_genrsa(dir: &std::path::Path) -> (String, String) {
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
    (
        std::fs::read_to_string(private_path).unwrap(),
        std::fs::read_to_string(public_path).unwrap(),
    )
}

struct TestServer {
    base: String,
    platform_token: String,
    tenant_admin_token: String,
    tenant_id: Uuid,
    pool: PgPool,
}

async fn boot_server() -> TestServer {
    let database_url = std::env::var("DATABASE_URL").expect("DATABASE_URL required for this e2e test");
    let pool = PgPoolOptions::new()
        .max_connections(5)
        .connect(&database_url)
        .await
        .unwrap();

    let platform_user_id = Uuid::new_v4();
    sqlx::query("INSERT INTO user_roles (tenant_id, user_id, role) VALUES ($1, $2, 'platform_admin')")
        .bind(metap_control::PLATFORM_TENANT_ID)
        .bind(platform_user_id)
        .execute(&pool)
        .await
        .unwrap();
    let tenant_id = Uuid::new_v4();
    let tenant_admin_id = Uuid::new_v4();
    sqlx::query("INSERT INTO user_roles (tenant_id, user_id, role) VALUES ($1, $2, 'admin')")
        .bind(tenant_id)
        .bind(tenant_admin_id)
        .execute(&pool)
        .await
        .unwrap();

    let keydir = TempDir::new();
    let (private_pem, public_pem) = openssl_genrsa(&keydir.0);
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

    TestServer {
        base: format!("http://{addr}"),
        platform_token: metap_peripherals::mint_jwt(
            &private_pem,
            metap_control::PLATFORM_TENANT_ID,
            platform_user_id,
            3600,
        )
        .unwrap(),
        tenant_admin_token: metap_peripherals::mint_jwt(&private_pem, tenant_id, tenant_admin_id, 3600).unwrap(),
        tenant_id,
        pool,
    }
}

async fn cleanup(server: &TestServer) {
    sqlx::query("DELETE FROM platform_configs")
        .execute(&server.pool)
        .await
        .ok();
    for tenant in [server.tenant_id, metap_control::PLATFORM_TENANT_ID] {
        sqlx::query("DELETE FROM user_roles WHERE tenant_id = $1")
            .bind(tenant)
            .execute(&server.pool)
            .await
            .ok();
    }
}

/// **The audit 04 A#1 regression, at the HTTP boundary.** A platform admin is the most privileged
/// caller this API has, and it must still be refused — these keys answer to whoever controls the
/// deployment's environment, not to anyone holding a token.
#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn an_operator_key_is_refused_even_for_a_platform_admin() {
    let server = boot_server().await;
    let client = reqwest::Client::new();

    for key in [
        "cron.webhookAllowPrivateTargets",
        "cron.webhookAllowedHosts",
        "http.corsOrigins",
    ] {
        let res = client
            .put(format!("{}/platform/config/{key}", server.base))
            .bearer_auth(&server.platform_token)
            .json(&json!({ "value": true }))
            .send()
            .await
            .unwrap();
        assert_eq!(res.status(), 403, "{key} must never be writable over HTTP");
        let body: serde_json::Value = res.json().await.unwrap();
        assert_eq!(body["error"]["code"], "config_key_not_writable");
    }

    // ...and it is not even listed, so the surface never advertises a value it cannot manage.
    let listed: serde_json::Value = client
        .get(format!("{}/platform/config", server.base))
        .bearer_auth(&server.platform_token)
        .send()
        .await
        .unwrap()
        .json()
        .await
        .unwrap();
    let keys: Vec<&str> = listed["data"]
        .as_array()
        .unwrap()
        .iter()
        .map(|item| item["key"].as_str().unwrap())
        .collect();
    assert!(!keys.iter().any(|k| k.starts_with("cron.")));
    assert!(!keys.contains(&"http.corsOrigins"));
    cleanup(&server).await;
}

#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn a_tenant_admin_cannot_reach_the_platform_surface_at_all() {
    let server = boot_server().await;
    let res = reqwest::Client::new()
        .get(format!("{}/platform/config", server.base))
        .bearer_auth(&server.tenant_admin_token)
        .send()
        .await
        .unwrap();
    assert_eq!(res.status(), 403);
    cleanup(&server).await;
}

/// An unset key must read back its declared default, not `null` — that is what makes this whole
/// layer additive over the hard-coded values it replaced.
#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn unset_keys_read_back_their_declared_defaults() {
    let server = boot_server().await;
    let listed: serde_json::Value = reqwest::Client::new()
        .get(format!("{}/platform/config", server.base))
        .bearer_auth(&server.platform_token)
        .send()
        .await
        .unwrap()
        .json()
        .await
        .unwrap();
    let find = |key: &str| -> serde_json::Value {
        listed["data"]
            .as_array()
            .unwrap()
            .iter()
            .find(|item| item["key"] == key)
            .expect("key must be listed")["value"]
            .clone()
    };
    assert_eq!(find("graphql.maxDepth"), 10);
    assert_eq!(find("graphql.maxComplexity"), 1000);
    assert_eq!(find("auth.sessionTtlSeconds"), 3600);
    assert_eq!(find("http.rateLimitPerMillisecond"), 200);
    assert_eq!(find("http.rateLimitBurst"), 300);
    cleanup(&server).await;
}

/// A set → read → reset round trip, plus the validator refusing an out-of-range value. The reset
/// half matters: clearing an override must restore the *declared default*, not whatever the value
/// happened to be before.
#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn setting_validating_and_resetting_a_platform_global_key() {
    let server = boot_server().await;
    let client = reqwest::Client::new();
    let url = format!("{}/platform/config/graphql.maxDepth", server.base);

    let set = client
        .put(&url)
        .bearer_auth(&server.platform_token)
        .json(&json!({ "value": 25 }))
        .send()
        .await
        .unwrap();
    assert_eq!(set.status(), 200);
    let body: serde_json::Value = set.json().await.unwrap();
    assert_eq!(body["data"]["value"], 25);
    assert_eq!(
        body["data"]["appliesImmediately"], true,
        "the GraphQL limits are read per use, not baked into a layer"
    );

    // Out of the declared range → 422, and the stored value is untouched.
    let rejected = client
        .put(&url)
        .bearer_auth(&server.platform_token)
        .json(&json!({ "value": 0 }))
        .send()
        .await
        .unwrap();
    assert_eq!(rejected.status(), 422);
    assert_eq!(
        rejected.json::<serde_json::Value>().await.unwrap()["error"]["code"],
        "invalid_config_value"
    );

    // An unknown key is an addressing mistake, not a validation one.
    let unknown = client
        .put(format!("{}/platform/config/nope.notAKey", server.base))
        .bearer_auth(&server.platform_token)
        .json(&json!({ "value": 1 }))
        .send()
        .await
        .unwrap();
    assert_eq!(unknown.status(), 404);

    let reset = client
        .delete(&url)
        .bearer_auth(&server.platform_token)
        .send()
        .await
        .unwrap();
    assert_eq!(reset.status(), 200);
    assert_eq!(reset.json::<serde_json::Value>().await.unwrap()["data"]["value"], 10);
    cleanup(&server).await;
}

/// The rate-limit keys are the one pair that needs a restart, and the response says so rather than
/// letting a caller assume the change already took effect.
#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn the_rate_limit_keys_report_that_they_need_a_restart() {
    let server = boot_server().await;
    let res = reqwest::Client::new()
        .put(format!("{}/platform/config/http.rateLimitBurst", server.base))
        .bearer_auth(&server.platform_token)
        .json(&json!({ "value": 500 }))
        .send()
        .await
        .unwrap();
    assert_eq!(res.status(), 200);
    assert_eq!(
        res.json::<serde_json::Value>().await.unwrap()["data"]["appliesImmediately"],
        false
    );
    cleanup(&server).await;
}
