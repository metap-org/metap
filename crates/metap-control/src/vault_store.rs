//! Second `SecretStore` impl (`docs/roadmap.md` Phase 16 Giai đoạn 4), after `EnvStore`. Static
//! KV v2 secrets over Vault's HTTP API via `vaultrs`. Two auth methods: a plain token
//! (`VaultStore::new`, `VAULT_TOKEN`) and AppRole (`VaultStore::new_with_approle`, added
//! 2026-08-20) — not Vault's dynamic database-credentials engine, still a real gap, left for
//! when a real production deployment target exists to need it
//! (`docs/architectures/07-deployment.md`'s secret-manager note has the same "no target yet"
//! shape). `DbCreds::expires_at` stays `None` here, same as `EnvStore` — nothing in this repo
//! produces a rotating credential yet.
//!
//! AppRole's practical win over a plain token isn't cryptographic strength (a Vault client
//! token is a Vault client token either way) — it's operational: `VAULT_TOKEN` means handing
//! every deployment a long-lived, directly-usable credential to distribute and rotate by hand;
//! AppRole's `role_id` is not secret (safe to bake into a deploy manifest) and its `secret_id`
//! is meant to be issued short-lived/one-time by whatever secrets-injection the deployment
//! already uses (Vault Agent, a CI step, Kubernetes injector), not hand-carried like a raw
//! token. `new_with_approle` logs in once at construction and calls `set_token` with the
//! resulting client token — **no renewal**: once that token's `lease_duration` elapses, every
//! subsequent Vault call fails and the process needs restarting (or this constructor calling
//! again) to log in fresh. Same lack-of-rotation posture `VaultStore::new`'s static token
//! already has (it never renews either); auto-renewal before expiry is deferred, real
//! deferred-not-forgotten work, not implemented here — needs a background task or
//! renew-on-failure retry, infrastructure this repo has no trigger for yet.

use async_trait::async_trait;
use secrecy::SecretString;
use serde::{Deserialize, Serialize};
use vaultrs::client::{Client, VaultClient, VaultClientSettingsBuilder};

use crate::secret_store::{DbCreds, SecretStore};

const MOUNT: &str = "secret";
const PATH_PREFIX: &str = "metap/dsn";
const DEFAULT_APPROLE_MOUNT: &str = "approle";

pub struct VaultStore {
    client: VaultClient,
}

impl VaultStore {
    pub fn new(addr: &str, token: &str) -> anyhow::Result<Self> {
        let settings = VaultClientSettingsBuilder::default()
            .address(addr)
            .token(token)
            .build()
            .map_err(|e| anyhow::anyhow!("invalid Vault client settings: {e}"))?;
        let client = VaultClient::new(settings).map_err(|e| anyhow::anyhow!("failed to build Vault client: {e}"))?;
        Ok(Self { client })
    }

    /// AppRole login (`role_id`/`secret_id`, see the module doc comment for why this exists
    /// alongside `new`) — `mount` is the AppRole auth backend's mount path, `"approle"` unless
    /// an operator has mounted it somewhere else.
    pub async fn new_with_approle(addr: &str, mount: &str, role_id: &str, secret_id: &str) -> anyhow::Result<Self> {
        let settings = VaultClientSettingsBuilder::default()
            .address(addr)
            .build()
            .map_err(|e| anyhow::anyhow!("invalid Vault client settings: {e}"))?;
        let mut client =
            VaultClient::new(settings).map_err(|e| anyhow::anyhow!("failed to build Vault client: {e}"))?;
        let auth_info = vaultrs::auth::approle::login(&client, mount, role_id, secret_id)
            .await
            .map_err(|e| anyhow::anyhow!("AppRole login failed against mount {mount}: {e}"))?;
        client.set_token(&auth_info.client_token);
        Ok(Self { client })
    }

    /// Same as [`Self::new_with_approle`] with the default `"approle"` mount.
    pub async fn new_with_default_approle(addr: &str, role_id: &str, secret_id: &str) -> anyhow::Result<Self> {
        Self::new_with_approle(addr, DEFAULT_APPROLE_MOUNT, role_id, secret_id).await
    }
}

#[derive(Serialize, Deserialize)]
struct DsnSecret {
    dsn: String,
}

impl VaultStore {
    /// Writes the DSN a `dsn_secret_ref` will resolve to — `dev-tools vault-put-dsn`'s only
    /// caller today (`crates/dev-tools/src/main.rs`), so an operator can populate Vault for a
    /// `dedicated_db` tenant. Not used by `Router`/`db_credentials` (read-only there) — same
    /// read-via-trait/write-via-inherent-method split `PostgresTenantRegistry` already has
    /// between `TenantRegistry::get` and `provision`/`set_status`.
    pub async fn put_dsn(&self, dsn_secret_ref: &str, dsn: &str) -> anyhow::Result<()> {
        let path = format!("{PATH_PREFIX}/{dsn_secret_ref}");
        vaultrs::kv2::set(&self.client, MOUNT, &path, &DsnSecret { dsn: dsn.to_string() })
            .await
            .map_err(|e| {
                anyhow::anyhow!("vault kv2 write failed for dsn_secret_ref {dsn_secret_ref} at {MOUNT}/{path}: {e}")
            })?;
        Ok(())
    }
}

#[async_trait]
impl SecretStore for VaultStore {
    async fn db_credentials(&self, dsn_secret_ref: &str) -> anyhow::Result<DbCreds> {
        let path = format!("{PATH_PREFIX}/{dsn_secret_ref}");
        let secret: DsnSecret = vaultrs::kv2::read(&self.client, MOUNT, &path).await.map_err(|e| {
            anyhow::anyhow!("vault kv2 read failed for dsn_secret_ref {dsn_secret_ref} at {MOUNT}/{path}: {e}")
        })?;
        Ok(DbCreds {
            dsn: SecretString::from(secret.dsn),
            expires_at: None,
        })
    }
}
