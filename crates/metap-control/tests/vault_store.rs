//! E2E test against a real dev Vault (see `docker-compose.yml`'s `vault` service:
//! `docker compose up -d vault`, dev mode with a fixed root token). `#[ignore]`d so a plain
//! `cargo test` never touches a network — run with `cargo test -p metap-control -- --ignored`.
//! No `DATABASE_URL` needed here, hence the name doesn't end in `_postgres` like this crate's
//! other e2e test files.
//!
//! The AppRole test below needs a role to already exist in dev Vault — not provisioned by this
//! file itself (`docker-compose.yml`'s dev Vault starts with no auth methods beyond `token/`),
//! same "operator sets this up once" posture as everything else Vault-side. One-time setup
//! against the running dev Vault container:
//! ```sh
//! docker compose exec -e VAULT_ADDR=http://localhost:8200 -e VAULT_TOKEN=metap-dev-root-token vault vault auth enable approle
//! docker compose exec -e VAULT_ADDR=http://localhost:8200 -e VAULT_TOKEN=metap-dev-root-token vault vault policy write metap-dsn-read - <<'EOF'
//! path "secret/data/metap/dsn/*" { capabilities = ["read"] }
//! EOF
//! docker compose exec -e VAULT_ADDR=http://localhost:8200 -e VAULT_TOKEN=metap-dev-root-token vault vault write auth/approle/role/metap-crm-server token_policies="metap-dsn-read" token_ttl=1h token_max_ttl=4h
//! ```
//! then export `VAULT_ROLE_ID`/`VAULT_SECRET_ID` from `vault read auth/approle/role/metap-crm-server/role-id`
//! / `vault write -f auth/approle/role/metap-crm-server/secret-id` before running this test.
//!
//! The renewal test below needs a *second* role using the same policy but a short `token_ttl`
//! (so the test can wait past a real expiry in a few seconds instead of an hour):
//! ```sh
//! docker compose exec -e VAULT_ADDR=http://localhost:8200 -e VAULT_TOKEN=metap-dev-root-token vault vault write auth/approle/role/metap-renew-test token_policies="metap-dsn-read" token_ttl=5s token_max_ttl=1h
//! ```
//! then export `VAULT_RENEW_ROLE_ID`/`VAULT_RENEW_SECRET_ID` the same way.

use metap_control::{SecretStore, VaultStore};
use secrecy::ExposeSecret;
use uuid::Uuid;

fn vault_addr() -> String {
    std::env::var("VAULT_ADDR").unwrap_or_else(|_| "http://localhost:8200".to_string())
}

fn vault_token() -> String {
    std::env::var("VAULT_TOKEN").unwrap_or_else(|_| "metap-dev-root-token".to_string())
}

#[tokio::test]
#[ignore = "e2e: requires VAULT_ADDR / a running dev Vault"]
async fn writes_a_dsn_then_reads_it_back() {
    let store = VaultStore::new(&vault_addr(), &vault_token()).expect("construct VaultStore");
    let dsn_secret_ref = format!("test_{}", Uuid::new_v4().simple());

    store
        .put_dsn(&dsn_secret_ref, "postgres://tenant:tenant@localhost:5433/tenant_db")
        .await
        .expect("put_dsn");

    let creds = store.db_credentials(&dsn_secret_ref).await.expect("db_credentials");
    assert_eq!(
        creds.dsn.expose_secret(),
        "postgres://tenant:tenant@localhost:5433/tenant_db"
    );
    assert!(creds.expires_at.is_none(), "static KV secret must not carry an expiry");
}

#[tokio::test]
#[ignore = "e2e: requires VAULT_ADDR / a running dev Vault"]
async fn missing_secret_fails_clearly() {
    let store = VaultStore::new(&vault_addr(), &vault_token()).expect("construct VaultStore");
    let never_written = format!("never_written_{}", Uuid::new_v4().simple());

    let err = store
        .db_credentials(&never_written)
        .await
        .expect_err("secret was never written");
    assert!(err.to_string().contains(&never_written));
}

#[tokio::test]
#[ignore = "e2e: requires VAULT_ADDR / a running dev Vault"]
async fn overwriting_an_existing_dsn_is_read_back_as_the_new_value() {
    let store = VaultStore::new(&vault_addr(), &vault_token()).expect("construct VaultStore");
    let dsn_secret_ref = format!("test_overwrite_{}", Uuid::new_v4().simple());

    store
        .put_dsn(&dsn_secret_ref, "postgres://first")
        .await
        .expect("first put_dsn");
    store
        .put_dsn(&dsn_secret_ref, "postgres://second")
        .await
        .expect("second put_dsn");

    let creds = store.db_credentials(&dsn_secret_ref).await.expect("db_credentials");
    assert_eq!(creds.dsn.expose_secret(), "postgres://second");
}

#[tokio::test]
#[ignore = "e2e: requires VAULT_ADDR / a running dev Vault with the AppRole setup in this file's doc comment"]
async fn approle_login_can_read_a_dsn_written_by_a_token_authed_store() {
    let role_id = std::env::var("VAULT_ROLE_ID").expect("VAULT_ROLE_ID required for this e2e test");
    let secret_id = std::env::var("VAULT_SECRET_ID").expect("VAULT_SECRET_ID required for this e2e test");

    // Written with the root-token store — this test only exercises AppRole on the read side,
    // matching what `Router`/`db_credentials` actually needs from it in production.
    let token_store = VaultStore::new(&vault_addr(), &vault_token()).expect("construct token-authed VaultStore");
    let dsn_secret_ref = format!("test_approle_{}", Uuid::new_v4().simple());
    token_store
        .put_dsn(&dsn_secret_ref, "postgres://approle-tenant@localhost:5433/tenant_db")
        .await
        .expect("put_dsn via token-authed store");

    let approle_store = VaultStore::new_with_default_approle(&vault_addr(), &role_id, &secret_id)
        .await
        .expect("AppRole login");
    let creds = approle_store
        .db_credentials(&dsn_secret_ref)
        .await
        .expect("db_credentials via AppRole-authed store");
    assert_eq!(
        creds.dsn.expose_secret(),
        "postgres://approle-tenant@localhost:5433/tenant_db"
    );
}

#[tokio::test]
#[ignore = "e2e: requires VAULT_ADDR / a running dev Vault, ~6s runtime (waits past a real token expiry)"]
async fn approle_token_auto_renews_before_a_real_expiry() {
    let role_id = std::env::var("VAULT_RENEW_ROLE_ID").expect("VAULT_RENEW_ROLE_ID required for this e2e test");
    let secret_id = std::env::var("VAULT_RENEW_SECRET_ID").expect("VAULT_RENEW_SECRET_ID required for this e2e test");

    let token_store = VaultStore::new(&vault_addr(), &vault_token()).expect("construct token-authed VaultStore");
    let dsn_secret_ref = format!("test_renew_{}", Uuid::new_v4().simple());
    token_store
        .put_dsn(&dsn_secret_ref, "postgres://renewed@localhost:5433/tenant_db")
        .await
        .expect("put_dsn via token-authed store");

    // `metap-renew-test`'s role has `token_ttl=5s` — with `RENEW_BUFFER` at 60s, the very first
    // call after login already takes the renewal path (constructed 5s < 60s buffer), so this
    // isn't really testing renewal yet on its own; it's the sleep past the *real* 5s TTL below
    // that proves it. Without auto-renewal, this call would hit Vault with a token it has
    // already expired server-side and fail.
    let approle_store = VaultStore::new_with_default_approle(&vault_addr(), &role_id, &secret_id)
        .await
        .expect("AppRole login");
    tokio::time::sleep(std::time::Duration::from_secs(6)).await;

    let creds = approle_store
        .db_credentials(&dsn_secret_ref)
        .await
        .expect("db_credentials must succeed past the original token's real expiry, via auto-renewal");
    assert_eq!(creds.dsn.expose_secret(), "postgres://renewed@localhost:5433/tenant_db");
}
