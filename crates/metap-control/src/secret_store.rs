//! `SecretStore` is a trait, not a concrete type, for the same reason `EventBus`
//! (`docs/architectures/09-adr.md`) and `PolicyStore` are — swapping how a `DedicatedDb`
//! tenant's connection string is resolved (env var today, Vault AppRole/KV or dynamic DB creds
//! later — `docs/multi-tenant-platform-design.md` §2.3) without touching `Router`.

use std::time::Instant;

use async_trait::async_trait;
use secrecy::SecretString;
use uuid::Uuid;

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

/// The JSON a general secret is stored as, in every backend that stores structured payloads —
/// deliberately the same shape family as the `{"dsn": "..."}` payload [`SecretStore::db_credentials`]
/// already reads, so an operator inspecting Vault/AWS/GCP by hand sees one mental model rather than
/// two. The key differs (`value`, not `dsn`) precisely so a DSN can never be read back as a generic
/// secret or the reverse by a store that confused its own two paths.
#[derive(serde::Serialize, serde::Deserialize)]
pub(crate) struct ValueSecret {
    pub(crate) value: String,
}

/// The reference under which one tenant's secret for one config key is stored — **derived, never
/// supplied**.
///
/// This is the single most important line of the webhook-secret feature
/// (`docs/features/18-config-tiers-db-backed.md` slice 3). The brief's requirement was that the
/// server prefix a caller's reference with the tenant id, so tenant A cannot read tenant B's secret
/// by guessing B's reference. Deriving the whole string instead is strictly stronger: there is no
/// request field anywhere that carries a reference, so there is nothing to prefix, validate, or get
/// wrong. Same discipline as `S3ObjectStore` building `{tenant_id}/{key}` internally.
///
/// The format is the intersection of what all four backends accept, which is why it looks like an
/// environment variable rather than a path: `EnvStore` needs a POSIX-ish name (`[A-Za-z0-9_]`), GCP
/// Secret Manager allows only `[A-Za-z0-9_-]`, and AWS/Vault are more permissive than both. One
/// format for four backends means a secret provisioned for a tenant resolves identically whichever
/// backend a deployment runs.
pub fn tenant_secret_ref(tenant_id: Uuid, key: &str) -> String {
    let key: String = key
        .chars()
        .map(|c| if c.is_ascii_alphanumeric() { c } else { '_' })
        .collect();
    format!(
        "METAP_TENANT_{}_{}",
        tenant_id.simple().to_string().to_ascii_uppercase(),
        key.to_ascii_uppercase()
    )
}

#[async_trait]
pub trait SecretStore: Send + Sync {
    /// `dsn_secret_ref` is `control.tenants.dsn_secret_ref` — `Router` already resolved the
    /// tenant's routing row before calling this, so it's passed the reference directly rather
    /// than a `TenantId` the store would have to look up again itself.
    async fn db_credentials(&self, dsn_secret_ref: &str) -> anyhow::Result<DbCreds>;

    /// Reads a general secret — today, the `Authorization` header value a tenant's cron webhook
    /// sends (`cron-scheduler`'s `executor::webhook`).
    ///
    /// `secret_ref` must come from [`tenant_secret_ref`], never from a request. Returns an error
    /// rather than `None` when nothing is stored: every caller is about to use the value for
    /// something, and "no credential" is a failure to report, not a case to paper over.
    async fn get_secret(&self, secret_ref: &str) -> anyhow::Result<SecretString>;

    /// Stores a general secret, replacing any previous value.
    ///
    /// A backend that cannot accept writes at runtime (see [`EnvStore`]) must return an error here
    /// rather than pretend — a secret store that silently loses what it was given is worse than one
    /// that refuses it.
    async fn put_secret(&self, secret_ref: &str, value: &str) -> anyhow::Result<()>;

    /// Removes a secret. Called when a tenant clears the config key that referenced it, so that
    /// clearing the reference genuinely revokes the credential rather than merely hiding it — the
    /// executor derives the same reference every run and would otherwise keep using a value nobody
    /// can see any more.
    async fn delete_secret(&self, secret_ref: &str) -> anyhow::Result<()>;
}

/// Reads the DSN straight from an environment variable named `dsn_secret_ref`, verbatim — no
/// naming transformation. The default `SecretStore`, and the one a deployment gets when none of
/// Vault/AWS/GCP is configured. `dev-tools provision-tenant dedicated_db` prints the exact env var
/// an operator needs to set for a newly provisioned tenant to become routable.
///
/// **Read-only for general secrets.** `get_secret` works — an operator can provision a tenant's
/// webhook credential by exporting the environment variable [`tenant_secret_ref`] names, and the
/// cron executor resolves it exactly as it would from Vault. `put_secret`/`delete_secret` refuse.
/// Writing here would mean `std::env::set_var` on the running process: invisible to every other
/// process, lost on restart, and impossible to audit — a tenant admin would set a credential
/// through the API, see success, and find it gone after the next deploy. Refusing turns that into
/// an error message naming the variable to set instead.
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

    async fn get_secret(&self, secret_ref: &str) -> anyhow::Result<SecretString> {
        let value = std::env::var(secret_ref)
            .map_err(|_| anyhow::anyhow!("env var {secret_ref} is not set (secret lookup)"))?;
        Ok(SecretString::from(value))
    }

    async fn put_secret(&self, secret_ref: &str, _value: &str) -> anyhow::Result<()> {
        anyhow::bail!(
            "the environment-variable secret backend is read-only: set {secret_ref} in this deployment's \
             environment, or configure Vault/AWS Secrets Manager/GCP Secret Manager to let tenants manage \
             their own secrets"
        )
    }

    async fn delete_secret(&self, secret_ref: &str) -> anyhow::Result<()> {
        anyhow::bail!(
            "the environment-variable secret backend is read-only: unset {secret_ref} in this deployment's \
             environment"
        )
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

    /// The reference is a pure function of (tenant, key) — which is what makes it impossible for
    /// one tenant's request to name another tenant's secret. Two different tenants must never
    /// collide, and the same tenant must be stable across processes.
    #[test]
    fn a_secret_ref_is_derived_from_the_tenant_and_never_collides() {
        let a = Uuid::new_v4();
        let b = Uuid::new_v4();
        assert_eq!(
            tenant_secret_ref(a, "cron.webhookAuthorization"),
            tenant_secret_ref(a, "cron.webhookAuthorization"),
            "must be stable"
        );
        assert_ne!(
            tenant_secret_ref(a, "cron.webhookAuthorization"),
            tenant_secret_ref(b, "cron.webhookAuthorization")
        );
        // Usable verbatim as an env var name, a GCP secret id and an AWS secret name.
        let reference = tenant_secret_ref(a, "cron.webhookAuthorization");
        assert!(
            reference
                .chars()
                .all(|c| c.is_ascii_uppercase() || c.is_ascii_digit() || c == '_'),
            "{reference} must be portable across all four backends"
        );
    }

    /// Refusing is the behavior under test, not an unimplemented stub — see [`EnvStore`]'s note.
    #[tokio::test]
    async fn env_store_refuses_to_write_a_secret_and_says_what_to_set_instead() {
        let err = EnvStore
            .put_secret("METAP_TENANT_X_Y", "s3cret")
            .await
            .expect_err("read-only backend");
        let message = err.to_string();
        assert!(message.contains("METAP_TENANT_X_Y"), "{message}");
        assert!(
            !message.contains("s3cret"),
            "the value must never reach an error string"
        );
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
