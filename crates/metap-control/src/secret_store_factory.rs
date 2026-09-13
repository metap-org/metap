//! Picks which `SecretStore` impl a binary should build — the one piece of the "which
//! `SecretStore` resolves a `DedicatedDb` tenant's DSN" wiring every binary that builds a
//! `Router` needs (`../metap-demo-crm`, `../metap-demo-jira`, `crates/reconciler-orchestrator`,
//! `crates/metap-dev-tools`). Centralized here (rather than each binary hand-rolling the same
//! branch, the way it worked when `VaultStore`/`EnvStore` were the only two options) once a third
//! and fourth backend (`AwsSecretsManagerStore`, `GcpSecretManagerStore`) made the branch big
//! enough that four independent copies would risk drifting — this crate is already a dependency
//! of every one of those binaries, so this adds no new dependency edge, just moves logic that was
//! duplicated into one place.

use std::sync::Arc;

use metap_infra::AppConfig;
use secrecy::SecretString;

use crate::aws_secrets_manager_store::{AwsSecretsManagerStore, AwsSecretsManagerStoreConfig};
use crate::gcp_secret_manager_store::GcpSecretManagerStore;
use crate::secret_store::{EnvStore, SecretStore};
use crate::vault_store::VaultStore;

/// Just the 10 env-derived fields `build_secret_store` actually reads — split out from
/// `metap_infra::AppConfig` (2026-09-13, audit 04 finding B7) so a binary with no Postgres/
/// RabbitMQ pool of its own (`crates/metap-graphql-gateway`, which needs a `SecretStore` to
/// resolve upstream service-account credentials without holding them as plaintext env vars) can
/// build one without also satisfying `AppConfig::from_env`'s unrelated `DATABASE_URL`/
/// `RABBITMQ_URL` requirements just to get here. `From<&AppConfig>` below is how every existing
/// caller (which already has a real `AppConfig`) keeps working with a one-line change at the call
/// site; [`SecretStoreConfig::from_env`] is the equivalent for a binary that has no `AppConfig`.
pub struct SecretStoreConfig {
    pub vault_addr: Option<String>,
    pub vault_token: Option<String>,
    pub vault_role_id: Option<String>,
    pub vault_secret_id: Option<String>,
    pub vault_approle_mount: Option<String>,
    pub aws_secrets_region: Option<String>,
    pub aws_secrets_access_key: Option<String>,
    pub aws_secrets_secret_key: Option<String>,
    pub aws_secrets_endpoint_url: Option<String>,
    pub gcp_secrets_project_id: Option<String>,
}

impl From<&AppConfig> for SecretStoreConfig {
    fn from(config: &AppConfig) -> Self {
        Self {
            vault_addr: config.vault_addr.clone(),
            vault_token: config.vault_token.clone(),
            vault_role_id: config.vault_role_id.clone(),
            vault_secret_id: config.vault_secret_id.clone(),
            vault_approle_mount: config.vault_approle_mount.clone(),
            aws_secrets_region: config.aws_secrets_region.clone(),
            aws_secrets_access_key: config.aws_secrets_access_key.clone(),
            aws_secrets_secret_key: config.aws_secrets_secret_key.clone(),
            aws_secrets_endpoint_url: config.aws_secrets_endpoint_url.clone(),
            gcp_secrets_project_id: config.gcp_secrets_project_id.clone(),
        }
    }
}

impl SecretStoreConfig {
    /// Reads the same 10 env vars `metap_infra::AppConfig::load_config` does, directly — for a
    /// binary that has no `AppConfig`/Postgres pool at all. Same names, so an operator's existing
    /// `VAULT_ADDR`/`AWS_SECRETS_REGION`/`GCP_SECRETS_PROJECT_ID`/... deployment config works
    /// unchanged regardless of which binary reads it.
    pub fn from_env() -> Self {
        use metap_runtime::env::optional;
        Self {
            vault_addr: optional("VAULT_ADDR"),
            vault_token: optional("VAULT_TOKEN"),
            vault_role_id: optional("VAULT_ROLE_ID"),
            vault_secret_id: optional("VAULT_SECRET_ID"),
            vault_approle_mount: optional("VAULT_APPROLE_MOUNT"),
            aws_secrets_region: optional("AWS_SECRETS_REGION"),
            aws_secrets_access_key: optional("AWS_SECRETS_ACCESS_KEY"),
            aws_secrets_secret_key: optional("AWS_SECRETS_SECRET_KEY"),
            aws_secrets_endpoint_url: optional("AWS_SECRETS_ENDPOINT_URL"),
            gcp_secrets_project_id: optional("GCP_SECRETS_PROJECT_ID"),
        }
    }
}

/// Precedence when more than one backend's env vars are somehow set at once: GCP, then AWS,
/// then Vault, then the `EnvStore` fallback — an arbitrary but fixed order (an operator
/// configuring two cloud secret managers at once for one deployment is a misconfiguration
/// either way; this just makes the outcome deterministic and documented rather than order of
/// `if`/`match` arm evaluation being the only place it's decided).
pub async fn build_secret_store(config: &SecretStoreConfig) -> anyhow::Result<Arc<dyn SecretStore>> {
    if let Some(project_id) = &config.gcp_secrets_project_id {
        return Ok(Arc::new(GcpSecretManagerStore::new(project_id.clone()).await?));
    }

    if let Some(region) = &config.aws_secrets_region {
        let access_key = config
            .aws_secrets_access_key
            .as_deref()
            .ok_or_else(|| anyhow::anyhow!("AWS_SECRETS_REGION is set but AWS_SECRETS_ACCESS_KEY is not"))?;
        let secret_key = config
            .aws_secrets_secret_key
            .as_deref()
            .ok_or_else(|| anyhow::anyhow!("AWS_SECRETS_REGION is set but AWS_SECRETS_SECRET_KEY is not"))?;
        return Ok(Arc::new(AwsSecretsManagerStore::new(AwsSecretsManagerStoreConfig {
            region: region.clone(),
            access_key: SecretString::from(access_key.to_string()),
            secret_key: SecretString::from(secret_key.to_string()),
            endpoint_url: config.aws_secrets_endpoint_url.clone(),
        })));
    }

    if let Some(addr) = &config.vault_addr {
        return match (&config.vault_role_id, &config.vault_secret_id, &config.vault_token) {
            (Some(role_id), Some(secret_id), _) => {
                let mount = config.vault_approle_mount.as_deref().unwrap_or("approle");
                Ok(Arc::new(
                    VaultStore::new_with_approle(addr, mount, role_id, secret_id).await?,
                ))
            }
            (_, _, Some(token)) => Ok(Arc::new(VaultStore::new(addr, token)?)),
            _ => anyhow::bail!("VAULT_ADDR is set but neither VAULT_TOKEN nor VAULT_ROLE_ID+VAULT_SECRET_ID is"),
        };
    }

    Ok(Arc::new(EnvStore))
}
