//! E2E test against a real dev Vault (see `docker-compose.yml`'s `vault` service:
//! `docker compose up -d vault`, dev mode with a fixed root token). `#[ignore]`d so a plain
//! `cargo test` never touches a network — run with `cargo test -p metap-control -- --ignored`.
//! No `DATABASE_URL` needed here, hence the name doesn't end in `_postgres` like this crate's
//! other e2e test files.

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
