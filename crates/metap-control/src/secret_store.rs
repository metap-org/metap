//! `SecretStore` is a trait, not a concrete type, for the same reason `EventBus`
//! (`docs/architectures/09-adr.md`) and `PolicyStore` are — swapping how a `DedicatedDb`
//! tenant's connection string is resolved (env var today, Vault AppRole/KV or dynamic DB creds
//! later — `docs/multi-tenant-platform-design.md` §2.3) without touching `Router`.

use std::time::Instant;

use async_trait::async_trait;
use secrecy::SecretString;

/// `#[derive(Debug)]` is safe here — `secrecy::SecretString`'s own `Debug` impl redacts its
/// contents (`Secret([REDACTED])`), it never prints the DSN.
#[derive(Debug)]
pub struct DbCreds {
    pub dsn: SecretString,
    /// `None` = static credential, never rotated (every case today — `EnvStore`). `Some` is for
    /// a future dynamic-credential `SecretStore` (Vault-issued, self-expiring) to signal
    /// `Router` it should refresh the cached `PgPool` before this instant — not implemented yet,
    /// nothing produces `Some` today.
    pub expires_at: Option<Instant>,
}

#[async_trait]
pub trait SecretStore: Send + Sync {
    /// `dsn_secret_ref` is `control.tenants.dsn_secret_ref` — `Router` already resolved the
    /// tenant's routing row before calling this, so it's passed the reference directly rather
    /// than a `TenantId` the store would have to look up again itself.
    async fn db_credentials(&self, dsn_secret_ref: &str) -> anyhow::Result<DbCreds>;
}

/// Reads the DSN straight from an environment variable named `dsn_secret_ref`, verbatim — no
/// naming transformation. The only `SecretStore` this repo has today. `dev-tools
/// provision-tenant dedicated_db` prints the exact env var an operator needs to set for a newly
/// provisioned tenant to become routable.
pub struct EnvStore;

#[async_trait]
impl SecretStore for EnvStore {
    async fn db_credentials(&self, dsn_secret_ref: &str) -> anyhow::Result<DbCreds> {
        let dsn = std::env::var(dsn_secret_ref)
            .map_err(|_| anyhow::anyhow!("env var {dsn_secret_ref} is not set (dsn_secret_ref lookup)"))?;
        Ok(DbCreds {
            dsn: SecretString::from(dsn),
            expires_at: None,
        })
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use secrecy::ExposeSecret;

    #[tokio::test]
    async fn env_store_reads_the_named_var() {
        std::env::set_var("METAP_CONTROL_TEST_DSN", "postgres://example/test");
        let creds = EnvStore
            .db_credentials("METAP_CONTROL_TEST_DSN")
            .await
            .expect("var is set");
        assert_eq!(creds.dsn.expose_secret(), "postgres://example/test");
        std::env::remove_var("METAP_CONTROL_TEST_DSN");
    }

    #[tokio::test]
    async fn env_store_errors_clearly_when_var_is_missing() {
        let err = EnvStore
            .db_credentials("METAP_CONTROL_TEST_DSN_MISSING")
            .await
            .expect_err("var is unset");
        assert!(err.to_string().contains("METAP_CONTROL_TEST_DSN_MISSING"));
    }
}
