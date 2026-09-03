//! `SecretStore` over AWS Secrets Manager (`docs/roadmap.md` Phase 8, cloud secret-manager
//! target requested alongside self-host Vault). Same construction style
//! `metap-storage::S3ObjectStore` already established for this codebase's other AWS-SDK-backed
//! store — explicit `Credentials`/`Region`/`Builder`, not the SDK's default credential-provider
//! chain, so a misconfigured environment fails loudly with a clear credential error instead of
//! silently picking up whatever ambient IAM role happens to be present.
//!
//! The secret's `SecretString` value is expected to be JSON, `{"dsn": "<connection-string>"}` —
//! the same shape `VaultStore`'s `DsnSecret` uses — so an operator has one mental model
//! regardless of which `SecretStore` backend a tenant's `dsn_secret_ref` actually resolves
//! against.

use async_trait::async_trait;
use aws_sdk_secretsmanager::config::{BehaviorVersion, Builder, Credentials, Region};
use aws_sdk_secretsmanager::Client;
use secrecy::{ExposeSecret, SecretString};
use serde::{Deserialize, Serialize};

use crate::secret_store::{DbCreds, SecretStore, ValueSecret};

pub struct AwsSecretsManagerStoreConfig {
    pub region: String,
    pub access_key: SecretString,
    pub secret_key: SecretString,
    /// LocalStack or another AWS-API-compatible test double — real AWS Secrets Manager leaves
    /// this `None` and lets the SDK resolve the standard regional endpoint.
    pub endpoint_url: Option<String>,
}

pub struct AwsSecretsManagerStore {
    client: Client,
}

#[derive(Serialize, Deserialize)]
struct DsnSecret {
    dsn: String,
}

impl AwsSecretsManagerStore {
    pub fn new(config: AwsSecretsManagerStoreConfig) -> Self {
        let credentials = Credentials::new(
            config.access_key.expose_secret(),
            config.secret_key.expose_secret(),
            None,
            None,
            "metap-control",
        );
        let mut builder = Builder::new()
            .region(Region::new(config.region))
            .credentials_provider(credentials)
            .behavior_version(BehaviorVersion::latest());
        if let Some(endpoint_url) = config.endpoint_url {
            builder = builder.endpoint_url(endpoint_url);
        }
        Self {
            client: Client::from_conf(builder.build()),
        }
    }

    /// Writes the DSN a `dsn_secret_ref` will resolve to — the AWS counterpart to
    /// `VaultStore::put_dsn`, `dev-tools aws-secrets-put-dsn`'s only caller. Creates the secret
    /// container on first write (idempotent: a second call for the same `dsn_secret_ref` just
    /// adds a new version, same "latest always wins" semantics `db_credentials` reads back).
    pub async fn put_dsn(&self, dsn_secret_ref: &str, dsn: &str) -> anyhow::Result<()> {
        let payload = serde_json::to_string(&DsnSecret { dsn: dsn.to_string() })?;

        // Best-effort: the container may already exist (a prior `put_dsn` call, or one
        // provisioned out-of-band) — `add_secret_version` below is the write that actually
        // matters and surfaces a real error clearly if the container genuinely doesn't exist
        // and couldn't be created.
        let _ = self.client.create_secret().name(dsn_secret_ref).send().await;

        self.client
            .put_secret_value()
            .secret_id(dsn_secret_ref)
            .secret_string(payload)
            .send()
            .await
            .map_err(|e| anyhow::anyhow!("AWS Secrets Manager put_secret_value failed for {dsn_secret_ref}: {e}"))?;
        Ok(())
    }
}

#[async_trait]
impl SecretStore for AwsSecretsManagerStore {
    async fn db_credentials(&self, dsn_secret_ref: &str) -> anyhow::Result<DbCreds> {
        let output = self
            .client
            .get_secret_value()
            .secret_id(dsn_secret_ref)
            .send()
            .await
            .map_err(|e| anyhow::anyhow!("AWS Secrets Manager get_secret_value failed for {dsn_secret_ref}: {e}"))?;
        let secret_string = output
            .secret_string()
            .ok_or_else(|| anyhow::anyhow!("AWS Secrets Manager secret {dsn_secret_ref} has no SecretString value"))?;
        let secret: DsnSecret = serde_json::from_str(secret_string).map_err(|e| {
            anyhow::anyhow!(
                "AWS Secrets Manager secret {dsn_secret_ref} is not the expected {{\"dsn\": ...}} JSON shape: {e}"
            )
        })?;
        Ok(DbCreds {
            dsn: SecretString::from(secret.dsn),
            expires_at: None,
        })
    }

    async fn get_secret(&self, secret_ref: &str) -> anyhow::Result<SecretString> {
        let output = self
            .client
            .get_secret_value()
            .secret_id(secret_ref)
            .send()
            .await
            .map_err(|e| anyhow::anyhow!("AWS Secrets Manager get_secret_value failed for {secret_ref}: {e}"))?;
        let secret_string = output
            .secret_string()
            .ok_or_else(|| anyhow::anyhow!("AWS Secrets Manager secret {secret_ref} has no SecretString value"))?;
        let secret: ValueSecret = serde_json::from_str(secret_string).map_err(|e| {
            anyhow::anyhow!(
                "AWS Secrets Manager secret {secret_ref} is not the expected {{\"value\": ...}} JSON shape: {e}"
            )
        })?;
        Ok(SecretString::from(secret.value))
    }

    async fn put_secret(&self, secret_ref: &str, value: &str) -> anyhow::Result<()> {
        let payload = serde_json::to_string(&ValueSecret {
            value: value.to_string(),
        })?;
        // Same best-effort create as `put_dsn`: the version write below is what actually matters.
        let _ = self.client.create_secret().name(secret_ref).send().await;
        self.client
            .put_secret_value()
            .secret_id(secret_ref)
            .secret_string(payload)
            .send()
            .await
            // Built from the reference only — the value must never reach an error string.
            .map_err(|e| anyhow::anyhow!("AWS Secrets Manager put_secret_value failed for {secret_ref}: {e}"))?;
        Ok(())
    }

    async fn delete_secret(&self, secret_ref: &str) -> anyhow::Result<()> {
        self.client
            .delete_secret()
            .secret_id(secret_ref)
            // Without this AWS keeps the secret recoverable for a 30-day window, during which the
            // same reference still resolves — which would make "the tenant revoked this credential"
            // untrue for a month.
            .force_delete_without_recovery(true)
            .send()
            .await
            .map_err(|e| anyhow::anyhow!("AWS Secrets Manager delete_secret failed for {secret_ref}: {e}"))?;
        Ok(())
    }
}
