//! `/admin/config` and `/public/config` end to end
//! (`../metap-docs/docs/features/18-config-tiers-db-backed.md`, slice 2).
//!
//! Three boundaries carry this file, and each is here because it is the one a later change would
//! most plausibly erode:
//!
//! - [`a_tenant_admin_cannot_write_an_operator_or_fleet_key`] — the audit 04 A#1 regression, now at
//!   the *tenant* surface. Slice 1 asserted a platform admin cannot reach the SSRF allowlist; the
//!   tenant surface is the one people actually ask to have keys added to.
//! - [`one_tenants_overrides_never_leak_into_another`] — the tenant comes from the token and from
//!   nowhere else, so this asserts the property rather than a filter.
//! - [`the_public_surface_serves_branding_and_nothing_else`] — an unauthenticated endpoint that
//!   reads per-tenant config is the riskiest thing slice 2 adds, so it is tested for what it must
//!   *not* say as much as for what it must.
//!
//! Harness mirrors `platform_config_postgres.rs`, duplicated locally per this repo's convention.
//! `#[ignore]`d — needs `DATABASE_URL`.

use std::process::Command;
use std::sync::Arc;

use arc_swap::ArcSwap;
use axum::Router;
use jsonwebtoken::DecodingKey;
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
    let dir = std::env::temp_dir().join(format!("metap-http-tenant-config-{}", Uuid::new_v4()));
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
    public_pem: String,
    platform_token: String,
    tenant_a: Uuid,
    tenant_a_admin: String,
    tenant_a_member: String,
    tenant_b: Uuid,
    tenant_b_admin: String,
    hostname: String,
    _keys: TempDir,
}

async fn grant(pool: &PgPool, tenant_id: Uuid, user_id: Uuid, role: &str) {
    sqlx::query("INSERT INTO user_roles (tenant_id, user_id, role) VALUES ($1, $2, $3)")
        .bind(tenant_id)
        .bind(user_id)
        .bind(role)
        .execute(pool)
        .await
        .unwrap();
}

async fn boot_server() -> TestServer {
    let database_url = std::env::var("DATABASE_URL").expect("DATABASE_URL required for this e2e test");
    let pool = PgPoolOptions::new()
        .max_connections(5)
        .connect(&database_url)
        .await
        .unwrap();

    let (keys, private_pem, public_pem) = openssl_genrsa();

    let platform_user = Uuid::new_v4();
    grant(
        &pool,
        metap_control::PLATFORM_TENANT_ID,
        platform_user,
        "platform_admin",
    )
    .await;
    let (tenant_a, tenant_a_admin_id, tenant_a_member_id) = (Uuid::new_v4(), Uuid::new_v4(), Uuid::new_v4());
    grant(&pool, tenant_a, tenant_a_admin_id, "admin").await;
    grant(&pool, tenant_a, tenant_a_member_id, "member").await;
    let (tenant_b, tenant_b_admin_id) = (Uuid::new_v4(), Uuid::new_v4());
    grant(&pool, tenant_b, tenant_b_admin_id, "admin").await;

    // A hostname pointing at tenant A. Registered directly rather than through
    // `dev-tools set-tenant-hostname` so the test doesn't shell out, but through the same function
    // that command calls — the `control.tenants` FK means tenant A needs a real row first.
    sqlx::query(
        "INSERT INTO control.tenants (id, tier, strategy, schema_name, status)
         VALUES ($1, 'free', 'schema', 'public', 'active') ON CONFLICT (id) DO NOTHING",
    )
    .bind(tenant_a)
    .execute(&pool)
    .await
    .unwrap();
    let hostname = format!("t{}.example.com", Uuid::new_v4().simple());
    metap_control::set_tenant_hostname(&pool, tenant_a, &hostname)
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
        public_pem,
        platform_token: mint(metap_control::PLATFORM_TENANT_ID, platform_user),
        tenant_a_admin: mint(tenant_a, tenant_a_admin_id),
        tenant_a_member: mint(tenant_a, tenant_a_member_id),
        tenant_b_admin: mint(tenant_b, tenant_b_admin_id),
        tenant_a,
        tenant_b,
        hostname,
        pool,
        _keys: keys,
    }
}

async fn cleanup(server: &TestServer) {
    for table in ["tenant_configs", "platform_configs"] {
        sqlx::query(&format!("DELETE FROM {table}"))
            .execute(&server.pool)
            .await
            .ok();
    }
    sqlx::query("DELETE FROM control.tenant_hostnames WHERE hostname = $1")
        .bind(&server.hostname)
        .execute(&server.pool)
        .await
        .ok();
    for tenant in [server.tenant_a, server.tenant_b, metap_control::PLATFORM_TENANT_ID] {
        sqlx::query("DELETE FROM user_roles WHERE tenant_id = $1")
            .bind(tenant)
            .execute(&server.pool)
            .await
            .ok();
    }
    sqlx::query("DELETE FROM control.tenants WHERE id = $1")
        .bind(server.tenant_a)
        .execute(&server.pool)
        .await
        .ok();
}

fn value_of(body: &Value, key: &str) -> Value {
    body["data"]
        .as_array()
        .unwrap()
        .iter()
        .find(|item| item["key"] == key)
        .unwrap_or_else(|| panic!("{key} not listed"))["value"]
        .clone()
}

async fn admin_config(server: &TestServer, token: &str) -> Value {
    reqwest::Client::new()
        .get(format!("{}/admin/config", server.base))
        .bearer_auth(token)
        .send()
        .await
        .unwrap()
        .json()
        .await
        .unwrap()
}

/// **The audit 04 A#1 regression at the tenant surface.** Slice 1 proved a platform admin cannot
/// reach the SSRF allowlist; a tenant admin is the caller people will actually ask to have keys
/// opened up for, so the same boundary is asserted from here too — along with the fleet-wide keys,
/// which a tenant must not be able to set for everyone else.
#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn a_tenant_admin_cannot_write_an_operator_or_fleet_key() {
    let server = boot_server().await;
    let client = reqwest::Client::new();

    for key in [
        "cron.webhookAllowPrivateTargets",
        "cron.webhookAllowedHosts",
        "http.corsOrigins",
        // Fleet-wide keys: legal to exist, but not this caller's to set.
        "graphql.maxDepth",
        "http.rateLimitBurst",
    ] {
        let res = client
            .put(format!("{}/admin/config/{key}", server.base))
            .bearer_auth(&server.tenant_a_admin)
            .json(&json!({ "value": true }))
            .send()
            .await
            .unwrap();
        assert_eq!(res.status(), 403, "{key} must not be writable by a tenant admin");
        assert_eq!(
            res.json::<Value>().await.unwrap()["error"]["code"],
            "config_key_not_writable"
        );
    }

    // ...and none of the Operator/fleet-wide keys above is even listed on this surface. Only the
    // two Operator-tier cron keys are checked by prefix-free name — `cron.webhookAuthorization`
    // is `Tenant`-tier and legitimately appears here (`ConfigStore::tenant_view` lists every
    // `Tenant`-level key), so a blanket `starts_with("cron.")` would wrongly fail once that key
    // exists.
    let listed = admin_config(&server, &server.tenant_a_admin).await;
    let keys: Vec<&str> = listed["data"]
        .as_array()
        .unwrap()
        .iter()
        .map(|i| i["key"].as_str().unwrap())
        .collect();
    assert!(!keys.contains(&"cron.webhookAllowPrivateTargets"));
    assert!(!keys.contains(&"cron.webhookAllowedHosts"));
    assert!(!keys.iter().any(|k| k.starts_with("graphql.")));
    cleanup(&server).await;
}

/// A non-admin member may *read* the effective config (it describes the app they are using) but
/// must not write it.
#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn a_member_can_read_but_not_write_tenant_config() {
    let server = boot_server().await;
    let listed = admin_config(&server, &server.tenant_a_member).await;
    assert!(!listed["data"].as_array().unwrap().is_empty());

    let res = reqwest::Client::new()
        .put(format!("{}/admin/config/theme.displayName", server.base))
        .bearer_auth(&server.tenant_a_member)
        .json(&json!({ "value": "Not Allowed" }))
        .send()
        .await
        .unwrap();
    assert_eq!(res.status(), 403);
    cleanup(&server).await;
}

/// The isolation property, asserted rather than assumed: the tenant is taken from the caller's
/// token, so there is no request shape that could name another one.
#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn one_tenants_overrides_never_leak_into_another() {
    let server = boot_server().await;
    let client = reqwest::Client::new();

    let set = client
        .put(format!("{}/admin/config/theme.displayName", server.base))
        .bearer_auth(&server.tenant_a_admin)
        .json(&json!({ "value": "Tenant A" }))
        .send()
        .await
        .unwrap();
    assert_eq!(set.status(), 200);

    assert_eq!(
        value_of(
            &admin_config(&server, &server.tenant_a_admin).await,
            "theme.displayName"
        ),
        json!("Tenant A")
    );
    assert_eq!(
        value_of(
            &admin_config(&server, &server.tenant_b_admin).await,
            "theme.displayName"
        ),
        json!(""),
        "tenant B must see the inherited value, never tenant A's"
    );
    cleanup(&server).await;
}

/// The full chain `declared default <- platform fleet default <- tenant override`, over HTTP, on a
/// key that is genuinely wired (session TTL is what `/auth/token` mints with).
#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn the_three_tiers_resolve_in_order() {
    let server = boot_server().await;
    let client = reqwest::Client::new();
    let key = "auth.sessionTtlSeconds";

    // Tier 1: nothing set anywhere -> the value declared in Rust.
    assert_eq!(
        value_of(&admin_config(&server, &server.tenant_a_admin).await, key),
        json!(3600)
    );

    // Tier 2: a platform admin sets the fleet default; every tenant inherits it.
    assert_eq!(
        client
            .put(format!("{}/platform/config/{key}", server.base))
            .bearer_auth(&server.platform_token)
            .json(&json!({ "value": 7200 }))
            .send()
            .await
            .unwrap()
            .status(),
        200
    );
    for token in [&server.tenant_a_admin, &server.tenant_b_admin] {
        assert_eq!(value_of(&admin_config(&server, token).await, key), json!(7200));
    }

    // Tier 3: tenant A overrides it for itself alone.
    assert_eq!(
        client
            .put(format!("{}/admin/config/{key}", server.base))
            .bearer_auth(&server.tenant_a_admin)
            .json(&json!({ "value": 900 }))
            .send()
            .await
            .unwrap()
            .status(),
        200
    );
    assert_eq!(
        value_of(&admin_config(&server, &server.tenant_a_admin).await, key),
        json!(900)
    );
    assert_eq!(
        value_of(&admin_config(&server, &server.tenant_b_admin).await, key),
        json!(7200),
        "tenant B still inherits the fleet default"
    );

    // Clearing the tenant override falls back to the fleet default, not to the declared one.
    assert_eq!(
        client
            .delete(format!("{}/admin/config/{key}", server.base))
            .bearer_auth(&server.tenant_a_admin)
            .send()
            .await
            .unwrap()
            .status(),
        200
    );
    assert_eq!(
        value_of(&admin_config(&server, &server.tenant_a_admin).await, key),
        json!(7200)
    );
    cleanup(&server).await;
}

/// Proof the tenant tier is *wired*, not just readable: a token minted after the override carries
/// the tenant's own expiry. Without this, `/admin/config` could report a TTL nothing acts on.
#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn a_tenants_session_ttl_override_changes_the_tokens_it_mints() {
    let server = boot_server().await;
    let issued_ttl = |token: String| {
        let public_pem = server.public_pem.clone();
        async move {
            let mut validation = jsonwebtoken::Validation::new(jsonwebtoken::Algorithm::RS256);
            validation.validate_aud = false;
            let data = jsonwebtoken::decode::<serde_json::Map<String, Value>>(
                &token,
                &DecodingKey::from_rsa_pem(public_pem.as_bytes()).unwrap(),
                &validation,
            )
            .unwrap();
            let exp = data.claims["exp"].as_i64().unwrap();
            exp - chrono::Utc::now().timestamp()
        }
    };

    async fn fetch_token(base: &str, bearer: &str) -> String {
        let body: Value = reqwest::Client::new()
            .get(format!("{base}/auth/token"))
            .bearer_auth(bearer)
            .send()
            .await
            .unwrap()
            .json()
            .await
            .unwrap();
        body["data"]["token"].as_str().unwrap().to_string()
    }

    let before = issued_ttl(fetch_token(&server.base, &server.tenant_a_admin).await).await;
    assert!(
        (3500..=3600).contains(&before),
        "expected the 3600s default, got {before}"
    );

    assert_eq!(
        reqwest::Client::new()
            .put(format!("{}/admin/config/auth.sessionTtlSeconds", server.base))
            .bearer_auth(&server.tenant_a_admin)
            .json(&json!({ "value": 600 }))
            .send()
            .await
            .unwrap()
            .status(),
        200
    );

    let after = issued_ttl(fetch_token(&server.base, &server.tenant_a_admin).await).await;
    assert!(
        (500..=600).contains(&after),
        "the tenant's own 600s TTL must reach the minted token, got {after}"
    );
    // Tenant B, which set nothing, is unaffected.
    let other = issued_ttl(fetch_token(&server.base, &server.tenant_b_admin).await).await;
    assert!(
        (3500..=3600).contains(&other),
        "tenant B should be untouched, got {other}"
    );
    cleanup(&server).await;
}

/// **The unauthenticated surface.** What it must not do matters more than what it does: no key
/// outside the declared-public allowlist, and no way to tell a registered hostname from an
/// unregistered one.
#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn the_public_surface_serves_branding_and_nothing_else() {
    let server = boot_server().await;
    let client = reqwest::Client::new();

    // Tenant A sets one public key and one that is emphatically not.
    for (key, value) in [
        ("theme.displayName", json!("Acme")),
        ("theme.primaryColor", json!("#0af")),
        ("auth.sessionTtlSeconds", json!(1800)),
    ] {
        assert_eq!(
            client
                .put(format!("{}/admin/config/{key}", server.base))
                .bearer_auth(&server.tenant_a_admin)
                .json(&json!({ "value": value }))
                .send()
                .await
                .unwrap()
                .status(),
            200,
            "setting {key}"
        );
    }

    let public: Value = client
        .get(format!("{}/public/config", server.base))
        .header("host", &server.hostname)
        .send()
        .await
        .unwrap()
        .json()
        .await
        .unwrap();
    assert_eq!(value_of(&public, "theme.displayName"), json!("Acme"));
    assert_eq!(value_of(&public, "theme.primaryColor"), json!("#0af"));

    let keys: Vec<&str> = public["data"]
        .as_array()
        .unwrap()
        .iter()
        .map(|i| i["key"].as_str().unwrap())
        .collect();
    assert_eq!(
        keys,
        vec!["theme.primaryColor", "theme.logoUrl", "theme.displayName"],
        "the public surface must serve exactly the declared-public keys"
    );
    assert!(
        !keys.contains(&"auth.sessionTtlSeconds"),
        "a non-public key must be absent entirely — not rendered, not forbidden"
    );

    // An unregistered hostname answers 200 with the fleet-wide values, not 404: answering
    // differently would make this a tenant-existence oracle for anyone who can set a Host header.
    let unknown: reqwest::Response = client
        .get(format!("{}/public/config", server.base))
        .header("host", "nobody-claims-this.example.com")
        .send()
        .await
        .unwrap();
    assert_eq!(unknown.status(), 200);
    let unknown: Value = unknown.json().await.unwrap();
    assert_eq!(value_of(&unknown, "theme.displayName"), json!(""));
    assert_eq!(
        unknown["data"].as_array().unwrap().len(),
        public["data"].as_array().unwrap().len(),
        "an unknown hostname must return the same shape, so the two are indistinguishable"
    );
    cleanup(&server).await;
}

/// A tenant cannot store a value that would be injected into the login page it is rendered on.
#[tokio::test]
#[ignore = "e2e: requires DATABASE_URL / a running dev Postgres"]
async fn injection_shaped_theme_values_are_refused_before_storage() {
    let server = boot_server().await;
    let client = reqwest::Client::new();

    for (key, value) in [
        ("theme.primaryColor", "#0af; background: url(https://evil.example/x)"),
        ("theme.logoUrl", "javascript:alert(1)"),
        ("theme.logoUrl", "data:image/svg+xml;base64,PHN2Zz4="),
    ] {
        let res = client
            .put(format!("{}/admin/config/{key}", server.base))
            .bearer_auth(&server.tenant_a_admin)
            .json(&json!({ "value": value }))
            .send()
            .await
            .unwrap();
        assert_eq!(res.status(), 422, "{key} = {value:?} must be refused");
        assert_eq!(
            res.json::<Value>().await.unwrap()["error"]["code"],
            "invalid_config_value"
        );
    }

    // Nothing was stored, so the public surface still serves the inherited values.
    let public: Value = client
        .get(format!("{}/public/config", server.base))
        .header("host", &server.hostname)
        .send()
        .await
        .unwrap()
        .json()
        .await
        .unwrap();
    assert_eq!(value_of(&public, "theme.logoUrl"), json!(""));
    cleanup(&server).await;
}
