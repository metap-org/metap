//! Picks which `SecretStore` impl a binary should build from `metap_infra::AppConfig` — the one
//! piece of the "which `SecretStore` resolves a `DedicatedDb` tenant's DSN" wiring every binary
//! that builds a `Router` needs (`apps/crm-server`, `apps/jira-server`,
//! `crates/reconciler-orchestrator`, `crates/dev-tools`). Centralized here (rather than each
//! binary hand-rolling the same branch, the way it worked when `VaultStore`/`EnvStore` were the
//! only two options) once a third and fourth backend (`AwsSecretsManagerStore`,
//! `GcpSecretManagerStore`) made the branch big enough that four independent copies would risk
//! drifting — this crate is already a dependency of every one of those binaries, so this adds no
//! new dependency edge, just moves logic that was duplicated into one place.

use std::sync::Arc;

use metap_infra::AppConfig;
use secrecy::SecretString;

use crate::aws_secrets_manager_store::{AwsSecretsManagerStore, AwsSecretsManagerStoreConfig};
use crate::gcp_secret_manager_store::GcpSecretManagerStore;
use crate::secret_store::{EnvStore, SecretStore};
use crate::vault_store::VaultStore;

/// Precedence when more than one backend's env vars are somehow set at once: GCP, then AWS,
/// then Vault, then the `EnvStore` fallback — an arbitrary but fixed order (an operator
/// configuring two cloud secret managers at once for one deployment is a misconfiguration
/// either way; this just makes the outcome deterministic and documented rather than order of
/// `if`/`match` arm evaluation being the only place it's decided).
pub async fn build_secret_store(config: &AppConfig) -> anyhow::Result<Arc<dyn SecretStore>> {
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
