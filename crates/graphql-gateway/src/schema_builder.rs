//! Fetches every upstream's `GET /metadata/entities`, merges the results into one
//! `MetadataRegistry`, and builds the composite `RecordBackend`/`Schema` this gateway actually
//! serves. This is the piece that turns "N separately-deployed microservices, each with its own
//! schema" into "one GraphQL schema" — the whole reason this crate exists rather than a caller
//! just hitting each service's own `metap-graphql-http` mount separately.

use std::collections::HashMap;
use std::sync::Arc;

use metap_crud::RecordBackend;
use metap_graphql::{build_schema, CompositeBackend, Schema, SchemaLimits};
use metap_grpc::GrpcBackend;
use metap_metadata::{EntityDefinition, EntityField, EntityWorkflow, MetadataRegistry};
use serde::Deserialize;

use crate::config::UpstreamConfig;

/// Mirrors the subset of `metap_metadata::EntitySummary` this gateway needs to reconstruct an
/// `EntityDefinition` from the wire. `EntitySummary` itself only derives `Serialize` (every
/// in-process caller only ever produces one, never parses one back from JSON) — `EntityField`/
/// `EntityWorkflow` already derive `Deserialize` and are reused directly; `list_views`/`version`
/// in the real response are simply ignored (unknown fields aren't rejected by default).
#[derive(Deserialize)]
#[serde(rename_all = "camelCase")]
struct RemoteEntitySummary {
    name: String,
    label: String,
    fields: Vec<EntityField>,
    #[serde(default)]
    workflow: Option<EntityWorkflow>,
}

#[derive(Deserialize)]
struct MetadataEntitiesResponse {
    data: Vec<RemoteEntitySummary>,
}

pub struct BuiltSchema {
    pub schema: Arc<Schema>,
    /// The same `CompositeBackend` baked into `schema` as schema-wide data — kept here too
    /// because per-request `Reference`-field batching (`with_request_data`'s `RecordLoader`)
    /// needs its own reference to it, exactly the same "schema-wide *and* per-request" split
    /// `metap-graphql-http::router` already has for `state.crud`.
    pub backend: Arc<dyn RecordBackend>,
    pub entity_count: usize,
}

pub async fn build(upstreams: &[UpstreamConfig]) -> anyhow::Result<BuiltSchema> {
    let http = metap_runtime::http_client::default_client();
    let mut registry = MetadataRegistry::new();
    let mut by_entity: HashMap<String, Arc<dyn RecordBackend>> = HashMap::new();

    for upstream in upstreams {
        tracing::info!(upstream = upstream.name, url = upstream.metadata_url, "fetching schema");
        let response: MetadataEntitiesResponse = http
            .get(&upstream.metadata_url)
            .bearer_auth(&upstream.service_jwt)
            .send()
            .await
            .map_err(|e| {
                anyhow::anyhow!(
                    "fetching {} from upstream '{}': {e}",
                    upstream.metadata_url,
                    upstream.name
                )
            })?
            .error_for_status()
            .map_err(|e| anyhow::anyhow!("upstream '{}' returned an error status: {e}", upstream.name))?
            .json()
            .await
            .map_err(|e| {
                anyhow::anyhow!(
                    "parsing {} response from upstream '{}': {e}",
                    upstream.metadata_url,
                    upstream.name
                )
            })?;

        // One `GrpcBackend` (one gRPC channel) per upstream, shared by every entity it owns —
        // not one per entity, since a `Channel` is a multiplexed connection, not a per-call one.
        let grpc_backend: Arc<dyn RecordBackend> = Arc::new(
            GrpcBackend::connect(upstream.grpc_addr.clone(), upstream.service_jwt.clone())
                .await
                .map_err(|e| {
                    anyhow::anyhow!(
                        "connecting to upstream '{}' gRPC at {}: {e}",
                        upstream.name,
                        upstream.grpc_addr
                    )
                })?,
        );

        for entity in response.data {
            let entity_name = entity.name.clone();
            let definition = EntityDefinition {
                name: entity.name,
                label: entity.label,
                // Never actually read: this gateway has no `CrudService`/`metap-reconciler` to
                // consult `table_name` against a real database. "records" is the one literal
                // `MetadataCompiler::validate` always accepts regardless of what physical table
                // (if any) the name would otherwise imply — confirmed via `compiler.rs`'s
                // `table_name_ok` check — so it's a safe placeholder here.
                table_name: "records".to_string(),
                fields: entity.fields,
                list_views: vec![],
                workflow: entity.workflow,
            };
            // `register` itself rejects a name already present in `registry` — this is what
            // catches two upstreams both claiming the same entity name, fail-fast at boot.
            registry.register(definition).map_err(|e| {
                anyhow::anyhow!(
                    "registering entity '{entity_name}' from upstream '{}': {e}",
                    upstream.name
                )
            })?;
            by_entity.insert(entity_name, grpc_backend.clone());
        }
    }

    registry.validate_references()?;
    let entity_count = registry.list_entities().len();
    let backend: Arc<dyn RecordBackend> = Arc::new(CompositeBackend::new(by_entity));
    let schema = build_schema(&registry, backend.clone(), SchemaLimits::default())?;

    Ok(BuiltSchema {
        schema: Arc::new(schema),
        backend,
        entity_count,
    })
}
