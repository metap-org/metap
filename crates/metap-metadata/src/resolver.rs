//! Pluggable per-tenant metadata resolution (`docs/features/15-tenant-scoped-lowcode-metadata.md`).
//!
//! Every app in this organization today shares one global `MetadataRegistry` — `metap-lowcode`
//! merges every *published* low-code entity into it regardless of which tenant published it, so
//! tenant A's low-code entity is visible (schema, OpenAPI/GraphQL discovery, CRUD) to tenant B
//! too. This trait is the seam a low-code control plane plugs a per-tenant registry into instead,
//! without `metap-crud`/`metap-http`/`metap-graphql-http` ever needing to know a low-code layer
//! exists at all.
//!
//! `None` (the default everywhere this isn't configured — `AppState.metadata_resolver`,
//! `CrudService`'s own field of the same name) means every tenant keeps sharing the single
//! `ArcSwap<MetadataRegistry>` snapshot, i.e. today's behavior, unchanged.
use std::sync::Arc;

use async_trait::async_trait;
use uuid::Uuid;

use crate::MetadataRegistry;

/// Resolves the metadata registry a given tenant should see.
///
/// A real implementation (`metap-lowcode`'s `TenantMetadataResolver`) merges a fixed,
/// code-authored `metadata_base` with that tenant's own enabled+published low-code entities,
/// cached per tenant — see that type for the caching/invalidation contract. This trait itself
/// makes no promise about caching; a caller resolves once per request/operation and reuses the
/// result, the same "one registry snapshot per call" discipline every `CrudService` method
/// already follows against the plain `ArcSwap` today.
#[async_trait]
pub trait MetadataResolver: Send + Sync {
    /// Returns the registry `tenant_id` should see right now.
    async fn resolve(&self, tenant_id: Uuid) -> anyhow::Result<Arc<MetadataRegistry>>;

    /// Drops any cached registry for `tenant_id`, forcing the next `resolve` to rebuild it. A
    /// resolver with no cache of its own can leave this a no-op.
    async fn invalidate(&self, tenant_id: Uuid);
}
