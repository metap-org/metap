//! Same shape as `metap_metadata::registry::RegistryError` — a small closed enum for the
//! domain-shaped rejections `publish`/`rollback`/`save_draft` can produce, plus a catch-all
//! for genuine infrastructure failure (DB errors) so callers use a single `Result` type
//! instead of a nested one. `crates/metap-lowcode-http/src/lib.rs` maps each variant to an
//! HTTP status, the same way `routes/cron.rs`/`routes/admin.rs` map their own domain errors.

use metap_metadata::MetadataValidationError;

#[derive(Debug)]
pub enum PublishError {
    /// No draft exists for this entity name — nothing to publish.
    NoDraft,
    /// This entity name is already taken by a code-authored entity (`apps/crm-server`'s
    /// `*_entity.rs` registrations) — DB-authored metadata can never shadow those.
    NameReservedByCodeEntity,
    /// Shape or cross-reference validation failed (duplicate field names, an `enum` field
    /// with no `enumValues`, a `refEntity` pointing at an entity that doesn't exist, ...).
    Invalid(MetadataValidationError),
    /// `rollback` was asked for a `versionNumber` that doesn't exist for this entity.
    VersionNotFound(i32),
    /// A DB error, or (in practice never reachable) a name collision surfaced by
    /// `MetadataRegistry::register` during cross-entity validation.
    Db(anyhow::Error),
}

impl std::fmt::Display for PublishError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            PublishError::NoDraft => write!(f, "no draft exists for this entity"),
            PublishError::NameReservedByCodeEntity => {
                write!(f, "entity name is reserved by a code-authored entity")
            }
            PublishError::Invalid(err) => write!(f, "{err}"),
            PublishError::VersionNotFound(v) => write!(f, "version {v} does not exist"),
            PublishError::Db(err) => write!(f, "{err}"),
        }
    }
}

impl std::error::Error for PublishError {}

impl From<sqlx::Error> for PublishError {
    fn from(err: sqlx::Error) -> Self {
        PublishError::Db(err.into())
    }
}

impl From<anyhow::Error> for PublishError {
    fn from(err: anyhow::Error) -> Self {
        PublishError::Db(err)
    }
}

impl From<metap_metadata::RegistryError> for PublishError {
    fn from(err: metap_metadata::RegistryError) -> Self {
        match err {
            metap_metadata::RegistryError::Validation(e) => PublishError::Invalid(e),
            metap_metadata::RegistryError::AlreadyRegistered(name) => PublishError::Db(anyhow::anyhow!(
                "unexpected name collision on \"{name}\" while validating"
            )),
        }
    }
}
