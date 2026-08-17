//! Second `SecretStore` impl (`docs/roadmap.md` Phase 16 Giai đoạn 4), after `EnvStore`. Static
//! KV v2 secrets over Vault's HTTP API via `vaultrs`, authenticated with a plain token
//! (`VAULT_TOKEN`) — not AppRole, not Vault's dynamic database-credentials engine. Both are
//! real gaps, left for when a real production deployment target exists to need them
//! (`docs/architectures/07-deployment.md`'s secret-manager note has the same "no target yet"
//! shape). `DbCreds::expires_at` stays `None` here, same as `EnvStore` — nothing in this repo
//! produces a rotating credential yet.

use async_trait::async_trait;
use secrecy::SecretString;
use serde::{Deserialize, Serialize};
use vaultrs::client::{VaultClient, VaultClientSettingsBuilder};

use crate::secret_store::{DbCreds, SecretStore};

const MOUNT: &str = "secret";
const PATH_PREFIX: &str = "metap/dsn";

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
