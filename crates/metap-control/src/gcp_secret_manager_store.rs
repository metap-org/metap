//! `SecretStore` over GCP Secret Manager (`docs/roadmap.md` Phase 8, cloud secret-manager target
//! requested alongside self-host Vault and `AwsSecretsManagerStore`). Unlike the AWS/S3-backed
//! stores in this codebase, GCP has no access-key/secret-key credential shape to construct
//! explicitly — its idiomatic identity mechanism is Application Default Credentials (a
//! service-account JSON key file named by `GOOGLE_APPLICATION_CREDENTIALS`, or the ambient
//! workload identity on GKE/Cloud Run/GCE), which `SecretManagerService::builder().build()`
//! resolves on its own. That's the one deliberate asymmetry with `AwsSecretsManagerStoreConfig`
//! here — everything else (the `{"dsn": "..."}` JSON payload shape, the `put_dsn`/`db_credentials`
//! split) mirrors it exactly, so an operator has one mental model across cloud backends.

use async_trait::async_trait;
use google_cloud_secretmanager_v1::client::SecretManagerService;
use google_cloud_secretmanager_v1::model::{replication, Replication, Secret, SecretPayload};
use secrecy::SecretString;
use serde::{Deserialize, Serialize};

use crate::secret_store::{DbCreds, SecretStore, ValueSecret};

pub struct GcpSecretManagerStore {
    client: SecretManagerService,
    project_id: String,
}

#[derive(Serialize, Deserialize)]
struct DsnSecret {
    dsn: String,
}

impl GcpSecretManagerStore {
    /// Builds the client via Application Default Credentials — see this module's doc comment
    /// for why there's no explicit-credentials constructor parameter the way
    /// `AwsSecretsManagerStore::new` has one.
    pub async fn new(project_id: impl Into<String>) -> anyhow::Result<Self> {
        let client = SecretManagerService::builder()
            .build()
            .await
            .map_err(|e| anyhow::anyhow!("failed to build GCP Secret Manager client: {e}"))?;
        Ok(Self {
            client,
            project_id: project_id.into(),
        })
    }

    fn secret_resource_name(&self, dsn_secret_ref: &str) -> String {
        format!("projects/{}/secrets/{}", self.project_id, dsn_secret_ref)
    }

    /// Writes the DSN a `dsn_secret_ref` will resolve to — the GCP counterpart to
    /// `VaultStore::put_dsn`/`AwsSecretsManagerStore::put_dsn`, `dev-tools gcp-secrets-put-dsn`'s
    /// only caller. Creates the secret container (with the automatic, multi-region replication
    /// policy — the simplest choice, no cross-region compliance requirement drove this) on first
    /// write if it doesn't already exist, then adds a new version — same "latest always wins"
    /// semantics `db_credentials`'s `versions/latest` read relies on.
    pub async fn put_dsn(&self, dsn_secret_ref: &str, dsn: &str) -> anyhow::Result<()> {
        let payload = serde_json::to_vec(&DsnSecret { dsn: dsn.to_string() })?;

        // Best-effort: the container may already exist (a prior `put_dsn` call, or one
        // provisioned out-of-band) — `add_secret_version` below is the write that actually
        // matters and surfaces a real error clearly if the container genuinely doesn't exist
        // and couldn't be created.
        let _ = self
            .client
            .create_secret()
            .set_parent(format!("projects/{}", self.project_id))
            .set_secret_id(dsn_secret_ref)
            .set_secret(Secret::new().set_replication(Replication::new().set_replication(Some(
                replication::Replication::Automatic(replication::Automatic::default().into()),
            ))))
            .send()
            .await;

        self.client
            .add_secret_version()
            .set_parent(self.secret_resource_name(dsn_secret_ref))
            .set_payload(SecretPayload::new().set_data(payload))
            .send()
            .await
            .map_err(|e| anyhow::anyhow!("GCP Secret Manager add_secret_version failed for {dsn_secret_ref}: {e}"))?;
        Ok(())
    }
}

#[async_trait]
impl SecretStore for GcpSecretManagerStore {
    async fn db_credentials(&self, dsn_secret_ref: &str) -> anyhow::Result<DbCreds> {
        let name = format!("{}/versions/latest", self.secret_resource_name(dsn_secret_ref));
        let response = self
            .client
            .access_secret_version()
            .set_name(name)
            .send()
            .await
            .map_err(|e| {
                anyhow::anyhow!("GCP Secret Manager access_secret_version failed for {dsn_secret_ref}: {e}")
            })?;
        let payload = response
            .payload
            .ok_or_else(|| anyhow::anyhow!("GCP Secret Manager secret {dsn_secret_ref} has no payload"))?;
        let secret: DsnSecret = serde_json::from_slice(&payload.data).map_err(|e| {
            anyhow::anyhow!(
                "GCP Secret Manager secret {dsn_secret_ref} is not the expected {{\"dsn\": ...}} JSON shape: {e}"
            )
        })?;
        Ok(DbCreds {
            dsn: SecretString::from(secret.dsn),
            expires_at: None,
        })
    }

    async fn get_secret(&self, secret_ref: &str) -> anyhow::Result<SecretString> {
        let name = format!("{}/versions/latest", self.secret_resource_name(secret_ref));
        let response = self
            .client
            .access_secret_version()
            .set_name(name)
            .send()
            .await
            .map_err(|e| anyhow::anyhow!("GCP Secret Manager access_secret_version failed for {secret_ref}: {e}"))?;
        let payload = response
            .payload
            .ok_or_else(|| anyhow::anyhow!("GCP Secret Manager secret {secret_ref} has no payload"))?;
        let secret: ValueSecret = serde_json::from_slice(&payload.data).map_err(|e| {
            anyhow::anyhow!(
                "GCP Secret Manager secret {secret_ref} is not the expected {{\"value\": ...}} JSON shape: {e}"
            )
        })?;
        Ok(SecretString::from(secret.value))
    }

    async fn put_secret(&self, secret_ref: &str, value: &str) -> anyhow::Result<()> {
        let payload = serde_json::to_vec(&ValueSecret {
            value: value.to_string(),
        })?;
        // Same best-effort create as `put_dsn`.
        let _ = self
            .client
            .create_secret()
            .set_parent(format!("projects/{}", self.project_id))
            .set_secret_id(secret_ref)
            .set_secret(Secret::new().set_replication(Replication::new().set_replication(Some(
                replication::Replication::Automatic(replication::Automatic::default().into()),
            ))))
            .send()
            .await;
        self.client
            .add_secret_version()
            .set_parent(self.secret_resource_name(secret_ref))
            .set_payload(SecretPayload::new().set_data(payload))
            .send()
            .await
            // Built from the reference only — the value must never reach an error string.
            .map_err(|e| anyhow::anyhow!("GCP Secret Manager add_secret_version failed for {secret_ref}: {e}"))?;
        Ok(())
    }

    async fn delete_secret(&self, secret_ref: &str) -> anyhow::Result<()> {
        self.client
            .delete_secret()
            .set_name(self.secret_resource_name(secret_ref))
            .send()
            .await
            .map_err(|e| anyhow::anyhow!("GCP Secret Manager delete_secret failed for {secret_ref}: {e}"))?;
        Ok(())
    }
}
