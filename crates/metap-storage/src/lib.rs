//! Object storage — a swappable trait, same shape as `metap-control`'s `SecretStore`
//! (`EnvStore`/`VaultStore`) and `metap-infra`'s `EventBus` (`RabbitEventBus`): one interface,
//! callers hold a `Box<dyn ObjectStore>`/`Arc<dyn ObjectStore>`, never the concrete type.
//!
//! `ObjectStore` deliberately doesn't expose S3 concepts (buckets, ETags, versioning,
//! multipart) — a plain key/bytes blob store, the smallest shape that still fits every backend
//! actually being considered (self-hosted MinIO successors like SeaweedFS/RustFS, a future local-
//! filesystem impl for a single-node deployment with no object-storage dependency, or AWS S3
//! itself). `S3ObjectStore` (this crate's only impl today) happens to be built on the S3 HTTP
//! API via `aws-sdk-s3`, which is itself already a de facto standard every self-hosted
//! alternative in this space speaks — see `docs/roadmap.md`'s storage-evaluation note — so
//! swapping *which* S3-compatible server sits behind it is just an `endpoint_url` config change,
//! no new impl needed; swapping to something that isn't S3-shaped at all (local disk, a
//! different cloud API) is a new `ObjectStore` impl, not a change to every caller.
//!
//! **Security-first, not caller discipline**: every method takes `tenant_id` as a mandatory
//! first parameter — this is a multi-tenant platform, and every other data-access seam here
//! (`Router::begin`, `CrudService`'s `WHERE tenant_id = $1`) bakes tenant scoping into the choke
//! point itself rather than trusting every future caller to prefix a key correctly. `S3ObjectStore`
//! turns `(tenant_id, key)` into the real S3 key (`{tenant_id}/{key}`) internally — a caller can
//! never construct a request that reads/writes another tenant's objects, and `validate_key`
//! rejects a key shaped to escape its own tenant prefix (`..` segments, a leading `/`) before it
//! ever reaches the backend, the same defense-in-depth discipline this codebase already applies
//! to `Router::validate_schema_name`/`CrudService`'s `table_name` validation — untrusted-shaped
//! input gets checked at the boundary, not trusted because "no caller does that today."
//!
//! No business-entity knowledge, no HTTP — a plain library, same shape as `metap-permission`.
//! Not wired into any route/feature yet — added ahead of a concrete consumer (e.g. entity file
//! attachments), the same order `metap-reconciler` was built in before `../metap-demo-jira` became
//! its first real caller. Whoever wires in the first consumer: `content_type` is caller-supplied
//! and stored verbatim — if objects are ever served back to a browser, don't trust it as safe to
//! render inline (serve with `Content-Disposition: attachment` or from a sandboxed origin,
//! standard defense against stored-content XSS via a user-uploaded `text/html`/`image/svg+xml`).

mod s3;

pub use s3::{S3ObjectStore, S3ObjectStoreConfig};

use async_trait::async_trait;
use bytes::Bytes;
use uuid::Uuid;

/// Rejects a key shaped to misbehave once prefixed with a tenant id — `..`/`.` path segments (an
/// S3 key has no real directory semantics server-side, but a traversal-shaped key can still
/// collide with a sibling tenant's prefix, e.g. `../other-tenant/secret`), a leading `/` (would
/// change where it lands relative to the tenant prefix), empty, over S3's own 1024-byte key
/// limit, or containing a control character (confuses any `Content-Disposition` filename a
/// future HTTP layer might derive from it). Checked once, centrally, rather than trusted because
/// no caller happens to misuse it today.
fn validate_key(key: &str) -> anyhow::Result<()> {
    anyhow::ensure!(!key.is_empty(), "object key must not be empty");
    anyhow::ensure!(key.len() <= 1024, "object key must be at most 1024 bytes");
    anyhow::ensure!(!key.starts_with('/'), "object key must not start with '/'");
    anyhow::ensure!(
        key.split('/').all(|segment| segment != ".." && segment != "."),
        "object key must not contain '.' or '..' path segments"
    );
    anyhow::ensure!(
        !key.chars().any(|c| c.is_control()),
        "object key must not contain control characters"
    );
    Ok(())
}

#[async_trait]
pub trait ObjectStore: Send + Sync {
    /// Overwrites whatever was already at `key`, if anything — same "last write wins" semantics
    /// every backend under consideration gives a plain `PUT` for free, so this trait doesn't
    /// pretend to offer anything stronger (no optimistic-concurrency/ETag param).
    async fn put(&self, tenant_id: Uuid, key: &str, data: Bytes, content_type: Option<&str>) -> anyhow::Result<()>;

    /// `Ok(None)` for "no object at this key" — a normal, expected outcome (not every caller
    /// treats a missing key as an error), never mixed into the `Err` channel the way a raw S3
    /// `NoSuchKey` fault would be.
    async fn get(&self, tenant_id: Uuid, key: &str) -> anyhow::Result<Option<Bytes>>;

    /// Deleting a key that doesn't exist is not an error — matches S3's own `DELETE` semantics
    /// (idempotent, no "not found" fault), so every backend can implement this the same way.
    async fn delete(&self, tenant_id: Uuid, key: &str) -> anyhow::Result<()>;

    async fn exists(&self, tenant_id: Uuid, key: &str) -> anyhow::Result<bool>;
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn validate_key_accepts_a_normal_key() {
        assert!(validate_key("avatars/user-123.png").is_ok());
    }

    #[test]
    fn validate_key_rejects_traversal_segments() {
        assert!(validate_key("../other-tenant/secret").is_err());
        assert!(validate_key("a/../b").is_err());
        assert!(validate_key("./a").is_err());
    }

    #[test]
    fn validate_key_rejects_leading_slash_empty_and_control_chars() {
        assert!(validate_key("/absolute").is_err());
        assert!(validate_key("").is_err());
        assert!(validate_key("a\0b").is_err());
        assert!(validate_key(&"a".repeat(1025)).is_err());
    }
}
