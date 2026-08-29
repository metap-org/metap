//! The `DataLoader` every `Reference` field resolver uses — the mandatory N+1-protection piece
//! of this schema (see this crate's root doc comment). Keyed by `(entity_name, id)` rather than
//! one `DataLoader` instance per entity: a single query can traverse `Reference` fields into
//! several different target entities in one tick, and `async_graphql::dataloader::DataLoader`
//! only ever batches within one instance, so one instance handling every entity — grouping by
//! `entity_name` internally before calling `RecordBackend::get_many` once per distinct target
//! entity in the batch — is what actually gets the batching benefit across a mixed-entity tick.
//!
//! One instance is constructed per GraphQL request (see `schema.rs`'s request-data wiring), not
//! shared across requests — it closes over that request's own `RequestContext`, so a cached
//! batch result can never leak across callers/tenants. Goes through `Arc<dyn RecordBackend>`
//! rather than `Arc<CrudService>` directly so `Reference` field batching works identically
//! whether the target entity lives in-process or behind a remote `GrpcBackend` (BFF gateway).

use std::collections::HashMap;
use std::sync::Arc;

use async_graphql::dataloader::Loader;
use metap_crud::{RecordBackend, RecordCapabilities, RecordDto};
use metap_permission::RequestContext;
use uuid::Uuid;

pub type RecordKey = (String, Uuid);

pub struct RecordLoader {
    pub backend: Arc<dyn RecordBackend>,
    pub context: RequestContext,
}

/// `anyhow::Error` isn't `Clone` (required by `Loader::Error`) — `DataLoader` clones a batch's
/// error into every pending request that was coalesced into it, so this wraps it in an `Arc`,
/// the standard way to make an arbitrary error type cheaply cloneable.
pub type LoaderError = Arc<anyhow::Error>;

impl Loader<RecordKey> for RecordLoader {
    type Value = (RecordDto, RecordCapabilities);
    type Error = LoaderError;

    async fn load(&self, keys: &[RecordKey]) -> Result<HashMap<RecordKey, Self::Value>, Self::Error> {
        let mut by_entity: HashMap<&str, Vec<Uuid>> = HashMap::new();
        for (entity_name, id) in keys {
            by_entity.entry(entity_name.as_str()).or_default().push(*id);
        }

        let mut result = HashMap::with_capacity(keys.len());
        for (entity_name, ids) in by_entity {
            let batch = self
                .backend
                .get_many(entity_name, &ids, &self.context)
                .await
                .map_err(Arc::new)?;
            let records = match batch {
                metap_crud::ServiceResult::Ok { data, .. } => data,
                // An entity-level 403/404 for the *whole batch* (e.g. the caller can't read this
                // target entity at all) — not one key's problem, so every key resolves to
                // "not found" (`None` downstream) rather than failing the whole GraphQL request;
                // a single denied/nonexistent reference is already expected to resolve to null.
                metap_crud::ServiceResult::Err { .. } => Vec::new(),
            };
            for (id, dto, capabilities) in records {
                result.insert((entity_name.to_string(), id), (dto, capabilities));
            }
        }
        Ok(result)
    }
}
