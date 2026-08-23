//! `ObjectStore` over the S3 HTTP API via `aws-sdk-s3` — works unmodified against SeaweedFS
//! (this project's current pick, `docs/roadmap.md`), and against any other S3-API-compatible
//! server (RustFS, Garage, real AWS S3, ...) by changing `S3ObjectStoreConfig` alone; nothing in
//! this file names SeaweedFS specifically.

use async_trait::async_trait;
use aws_sdk_s3::config::{Credentials, Region};
use aws_sdk_s3::primitives::ByteStream;
use aws_sdk_s3::Client;
use bytes::Bytes;
use secrecy::{ExposeSecret, SecretString};
use uuid::Uuid;

use crate::{validate_key, ObjectStore};

pub struct S3ObjectStoreConfig {
    /// e.g. `"http://localhost:8333"` for a local SeaweedFS S3 gateway. Real AWS S3 leaves this
    /// unset (`None`) and lets the SDK resolve the standard AWS endpoint for `region`.
    pub endpoint_url: Option<String>,
    /// Every self-hosted S3-compatible server under consideration ignores the region value
    /// itself but the SDK still requires one — any non-empty string works against them; only
    /// matters for real AWS S3.
    pub region: String,
    /// `SecretString`, not `String` — same reasoning `metap-control::DbCreds.dsn` already
    /// established for a connection secret: its `Debug`/`Display` impls redact the contents, so
    /// an accidental `{:?}` in a log line or panic message can't leak it.
    pub access_key: SecretString,
    pub secret_key: SecretString,
    pub bucket: String,
    /// SeaweedFS (and most self-hosted S3-compatible servers) need path-style addressing
    /// (`http://host/bucket/key`) — they don't do virtual-hosted-style DNS
    /// (`http://bucket.host/key`) the way real AWS S3 does by default. `true` for SeaweedFS.
    pub force_path_style: bool,
}

pub struct S3ObjectStore {
    client: Client,
    bucket: String,
}

impl S3ObjectStore {
    pub fn new(config: S3ObjectStoreConfig) -> Self {
        let credentials = Credentials::new(
            config.access_key.expose_secret(),
            config.secret_key.expose_secret(),
            None,
            None,
            "metap-storage",
        );
        let mut builder = aws_sdk_s3::config::Builder::new()
            .region(Region::new(config.region))
            .credentials_provider(credentials)
            .force_path_style(config.force_path_style)
            .behavior_version(aws_sdk_s3::config::BehaviorVersion::latest());
        if let Some(endpoint_url) = config.endpoint_url {
            builder = builder.endpoint_url(endpoint_url);
        }
        Self {
            client: Client::from_conf(builder.build()),
            bucket: config.bucket,
        }
    }

    /// The real S3 key for `(tenant_id, key)` — every call site goes through this, never
    /// interpolates `key` directly, so tenant scoping can't be bypassed by a call site that
    /// forgets to prefix it itself (see this crate's top doc comment).
    fn scoped_key(tenant_id: Uuid, key: &str) -> anyhow::Result<String> {
        validate_key(key)?;
        Ok(format!("{tenant_id}/{key}"))
    }
}

fn is_not_found(err: &aws_sdk_s3::error::SdkError<impl std::error::Error>) -> bool {
    // Every backend under consideration is expected to return a real S3 fault code
    // (`NoSuchKey`/`NotFound`) for a missing object — checking the HTTP status is the one thing
    // guaranteed stable across SDK error-type changes and across backends that don't bother
    // populating the exact S3 fault code, unlike matching on a specific typed error variant.
    matches!(err.raw_response().map(|r| r.status().as_u16()), Some(404))
}

#[async_trait]
impl ObjectStore for S3ObjectStore {
    async fn put(&self, tenant_id: Uuid, key: &str, data: Bytes, content_type: Option<&str>) -> anyhow::Result<()> {
        let key = Self::scoped_key(tenant_id, key)?;
        let mut req = self
            .client
            .put_object()
            .bucket(&self.bucket)
            .key(key)
            .body(ByteStream::from(data));
        if let Some(content_type) = content_type {
            req = req.content_type(content_type);
        }
        req.send().await?;
        Ok(())
    }

    async fn get(&self, tenant_id: Uuid, key: &str) -> anyhow::Result<Option<Bytes>> {
        let key = Self::scoped_key(tenant_id, key)?;
        let result = self.client.get_object().bucket(&self.bucket).key(key).send().await;
        let output = match result {
            Ok(output) => output,
            Err(err) if is_not_found(&err) => return Ok(None),
            Err(err) => return Err(err.into()),
        };
        let bytes = output.body.collect().await?.into_bytes();
        Ok(Some(bytes))
    }

    async fn delete(&self, tenant_id: Uuid, key: &str) -> anyhow::Result<()> {
        let key = Self::scoped_key(tenant_id, key)?;
        self.client.delete_object().bucket(&self.bucket).key(key).send().await?;
        Ok(())
    }

    async fn exists(&self, tenant_id: Uuid, key: &str) -> anyhow::Result<bool> {
        let key = Self::scoped_key(tenant_id, key)?;
        let result = self.client.head_object().bucket(&self.bucket).key(key).send().await;
        match result {
            Ok(_) => Ok(true),
            Err(err) if is_not_found(&err) => Ok(false),
            Err(err) => Err(err.into()),
        }
    }
}
