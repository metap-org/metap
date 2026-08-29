//! Routes a `RecordBackend` call to the right upstream by entity name — the piece that turns a
//! single `MetadataRegistry` assembled from *several* separately-deployed services'
//! `/metadata/entities` (see `crates/graphql-gateway`) into one schema that can still reach each
//! entity's real data. Entity-name routing is BFF-gateway-specific (a single-service binary like
//! `jira-server` has no need for it — it passes its own `Arc<CrudService>` straight through as
//! `Arc<dyn RecordBackend>`), so this lives here rather than in `metap-crud` alongside the trait
//! itself.

use std::collections::HashMap;
use std::sync::Arc;

use anyhow::anyhow;
use metap_crud::{JsonObject, RecordBackend, RecordCapabilities, RecordDto, ServiceResult};
use metap_permission::RequestContext;
use metap_query::ListInput;
use uuid::Uuid;

/// Maps an entity name to the `RecordBackend` that actually owns it. Built once at boot from the
/// gateway's upstream configuration and never mutated afterward — a `CompositeBackend` instance is
/// as static as the `MetadataRegistry` it was built alongside.
pub struct CompositeBackend {
    by_entity: HashMap<String, Arc<dyn RecordBackend>>,
}

impl CompositeBackend {
    pub fn new(by_entity: HashMap<String, Arc<dyn RecordBackend>>) -> Self {
        Self { by_entity }
    }

    /// An entity missing from the map means the gateway's own boot-time wiring is wrong (every
    /// entity in the schema was, by construction, registered from exactly one upstream) — not a
    /// caller error, so this is an `anyhow` error like every other unexpected `RecordBackend`
    /// failure, not a `ServiceResult::Err` (which is reserved for expected, caller-facing
    /// outcomes like permission/validation failures).
    fn resolve(&self, entity: &str) -> anyhow::Result<&Arc<dyn RecordBackend>> {
        self.by_entity
            .get(entity)
            .ok_or_else(|| anyhow!("no backend registered for entity '{entity}' — gateway wiring bug"))
    }
}

#[async_trait::async_trait]
impl RecordBackend for CompositeBackend {
    async fn list(
        &self,
        entity: &str,
        input: &ListInput,
        ctx: &RequestContext,
    ) -> anyhow::Result<ServiceResult<Vec<RecordDto>>> {
        self.resolve(entity)?.list(entity, input, ctx).await
    }

    async fn get(
        &self,
        entity: &str,
        id: Uuid,
        ctx: &RequestContext,
    ) -> anyhow::Result<ServiceResult<(RecordDto, RecordCapabilities)>> {
        self.resolve(entity)?.get(entity, id, ctx).await
    }

    async fn get_many(
        &self,
        entity: &str,
        ids: &[Uuid],
        ctx: &RequestContext,
    ) -> anyhow::Result<ServiceResult<Vec<(Uuid, RecordDto, RecordCapabilities)>>> {
        self.resolve(entity)?.get_many(entity, ids, ctx).await
    }

    async fn create(
        &self,
        entity: &str,
        data: &JsonObject,
        ctx: &RequestContext,
    ) -> anyhow::Result<ServiceResult<RecordDto>> {
        self.resolve(entity)?.create(entity, data, ctx).await
    }

    async fn update(
        &self,
        entity: &str,
        id: Uuid,
        expected_version: i32,
        data: &JsonObject,
        ctx: &RequestContext,
    ) -> anyhow::Result<ServiceResult<RecordDto>> {
        self.resolve(entity)?
            .update(entity, id, expected_version, data, ctx)
            .await
    }

    async fn transition(
        &self,
        entity: &str,
        id: Uuid,
        action: &str,
        expected_version: i32,
        data: Option<&JsonObject>,
        ctx: &RequestContext,
    ) -> anyhow::Result<ServiceResult<RecordDto>> {
        self.resolve(entity)?
            .transition(entity, id, action, expected_version, data, ctx)
            .await
    }

    async fn delete(
        &self,
        entity: &str,
        id: Uuid,
        expected_version: i32,
        ctx: &RequestContext,
    ) -> anyhow::Result<ServiceResult<RecordDto>> {
        self.resolve(entity)?.delete(entity, id, expected_version, ctx).await
    }
}
