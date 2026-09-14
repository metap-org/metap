//! Fetches every upstream's `GET /metadata/entities`, merges the results into one
//! `MetadataRegistry`, and builds the composite `RecordBackend`/`Schema` this gateway actually
//! serves. This is the piece that turns "N separately-deployed microservices, each with its own
//! schema" into "one GraphQL schema" — the whole reason this crate exists rather than a caller
//! just hitting each service's own `metap-graphql-http` mount separately.
//!
//! **Audit 04 finding B1, fixed 2026-09-13.** This used to discover every upstream exactly once
//! at boot, `?`-chained end to end — one slow/misconfigured upstream failed the ENTIRE gateway's
//! boot, and a newly-published low-code entity never appeared here until a manual restart,
//! directly contradicting the platform's own "hot-swap metadata, no restart" design promise (see
//! `metap_config`'s own doc comment for that promise elsewhere in this platform). Now: each
//! upstream is tracked by its own [`UpstreamCache`] (TTL-gated refresh, `moka::future::Cache`,
//! same pattern as `metap_control::RegistryCache`) that never forgets the last schema it
//! successfully saw, and [`GatewaySchemaCache`] rebuilds the composite schema from whatever every
//! `UpstreamCache` currently holds — an upstream that has never once succeeded contributes
//! nothing (its entities are simply absent, and any `Reference` field elsewhere pointing at one
//! of them is dropped, see [`build_composite`]'s doc comment), rather than blocking every other
//! upstream's entities from ever being served.

use std::collections::{HashMap, HashSet};
use std::sync::Arc;
use std::time::Duration;

use arc_swap::{ArcSwap, ArcSwapOption};
use async_graphql::dynamic::{Field, FieldFuture, Object};
use metap_crud::RecordBackend;
use metap_graphql::{
    build_schema_parts, CompositeBackend, FieldValue, GqlError, GqlValue, Schema, SchemaLimits, TypeRef, JSON_SCALAR,
};
use metap_grpc::GrpcBackend;
use metap_metadata::{EntityDefinition, EntityField, EntityWorkflow, FieldKind, MetadataRegistry};
use moka::future::Cache;
use serde::{Deserialize, Serialize};

use crate::config::UpstreamConfig;

/// A caller's `extend` closure (see [`build_with_extensions`]'s doc comment), type-erased and
/// shared across every `GatewaySchemaCache::current` refresh cycle.
type ExtendFn = dyn Fn(Object, Object) -> (Object, Object) + Send + Sync;

/// Same order of magnitude as this platform's other hot-swap caches
/// (`metap_control::RegistryCache`, `metap_config::ConfigStore`'s tenant tier) — a staleness
/// window this short is an acceptable tradeoff against re-fetching every upstream's schema on
/// every single request.
const TTL: Duration = Duration::from_secs(30);

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
    #[serde(default)]
    unique_constraints: Vec<metap_metadata::EntityUniqueConstraint>,
}

#[derive(Deserialize)]
struct MetadataEntitiesResponse {
    data: Vec<RemoteEntitySummary>,
}

/// One upstream's discovered entities plus the single `GrpcBackend` (one multiplexed channel,
/// shared by every entity that upstream owns) every one of those entities routes through.
struct UpstreamSchema {
    entities: Vec<EntityDefinition>,
    backend: Arc<dyn RecordBackend>,
}

/// A snapshot of one upstream's reachability — what `GET /health` and the GraphQL
/// `_gatewayHealth` field both report, and what a caller reads to know whether a `Reference`
/// field elsewhere might have been dropped because of this upstream.
#[derive(Debug, Clone, Serialize)]
pub struct UpstreamStatus {
    pub name: String,
    pub reachable: bool,
    pub error: Option<String>,
}

/// One `Reference` field this refresh cycle dropped from the composite schema because its
/// `ref_entity` belongs to an upstream that has never once been reachable — see
/// [`build_composite`]'s doc comment for the exact rule.
#[derive(Debug, Clone, Serialize)]
pub struct DroppedFieldReport {
    pub entity: String,
    pub field: String,
    pub ref_entity: String,
}

#[derive(Debug, Clone, Serialize)]
pub struct GatewayHealth {
    pub upstreams: Vec<UpstreamStatus>,
    pub dropped_fields: Vec<DroppedFieldReport>,
}

impl GatewayHealth {
    pub fn degraded(&self) -> bool {
        !self.upstreams.iter().all(|u| u.reachable)
    }
}

/// The composite schema this gateway currently serves, plus the health snapshot it was built
/// from — replaces the old `BuiltSchema` (which held only `schema`/`backend`/`entity_count`, all
/// still present here unchanged; `health` is the only addition).
pub struct CompositeSchema {
    pub schema: Arc<Schema>,
    /// The same `CompositeBackend` baked into `schema` as schema-wide data — kept here too
    /// because per-request `Reference`-field batching (`with_request_data`'s `RecordLoader`)
    /// needs its own reference to it, exactly the same "schema-wide *and* per-request" split
    /// `metap-graphql-http::router` already has for `state.crud`.
    pub backend: Arc<dyn RecordBackend>,
    pub entity_count: usize,
    pub health: GatewayHealth,
}

/// **Credential resolution (audit 04 finding B7, fixed 2026-09-13):** when
/// `config.service_password_secret_ref` is set, the real password is resolved fresh from
/// `secret_store` on every call — i.e. every TTL refresh cycle, not just once at boot — instead
/// of `config.service_password`'s literal env-var value. This is what lets an operator rotate an
/// upstream's credential in Vault/AWS/GCP Secrets Manager and have it take effect within one TTL
/// window, no redeploy — the same rotation story every other `SecretStore` consumer in this
/// codebase already has, closing the one place this gateway still held a plaintext,
/// never-rotated credential. A free function (not inlined into `connect_one_upstream`) so this
/// precedence is unit-testable without a real login/HTTP call, mirroring
/// `metap_grpc::client::pick_token`'s own reason for being split out.
async fn resolve_service_password(
    config: &UpstreamConfig,
    secret_store: &dyn metap_control::SecretStore,
) -> anyhow::Result<String> {
    use secrecy::ExposeSecret;

    match &config.service_password_secret_ref {
        Some(secret_ref) => secret_store
            .get_secret(secret_ref)
            .await
            .map(|s| s.expose_secret().to_string())
            .map_err(|e| {
                anyhow::anyhow!(
                    "resolving service_password_secret_ref for upstream '{}': {e}",
                    config.name
                )
            }),
        None => config.service_password.clone().ok_or_else(|| {
            anyhow::anyhow!(
                "upstream '{}' has neither service_password nor service_password_secret_ref configured",
                config.name
            )
        }),
    }
}

/// Logs into `config.login_url`, fetches `config.metadata_url`, and connects a `GrpcBackend` to
/// `config.grpc_addr` — the full "get one upstream's current schema" sequence, extracted so
/// [`UpstreamCache::refresh`] can retry it independently of every other upstream. Identical to
/// what this function used to do inline inside a single boot-time loop before audit 04 B1's fix.
async fn connect_one_upstream(
    config: &UpstreamConfig,
    http: &reqwest::Client,
    secret_store: &Arc<dyn metap_control::SecretStore>,
) -> anyhow::Result<UpstreamSchema> {
    let service_password = resolve_service_password(config, secret_store.as_ref()).await?;

    tracing::info!(upstream = config.name, url = config.login_url, "logging in");
    let service_token = metap_grpc::ServiceTokenSource::start(
        http.clone(),
        config.login_url.clone(),
        config.service_email.clone(),
        service_password,
    )
    .await
    .map_err(|e| anyhow::anyhow!("logging into upstream '{}' at {}: {e}", config.name, config.login_url))?;

    tracing::info!(upstream = config.name, url = config.metadata_url, "fetching schema");
    let response: MetadataEntitiesResponse = http
        .get(&config.metadata_url)
        .bearer_auth(&*service_token.current())
        .send()
        .await
        .map_err(|e| anyhow::anyhow!("fetching {} from upstream '{}': {e}", config.metadata_url, config.name))?
        .error_for_status()
        .map_err(|e| anyhow::anyhow!("upstream '{}' returned an error status: {e}", config.name))?
        .json()
        .await
        .map_err(|e| {
            anyhow::anyhow!(
                "parsing {} response from upstream '{}': {e}",
                config.metadata_url,
                config.name
            )
        })?;

    // One `GrpcBackend` (one gRPC channel) per upstream, shared by every entity it owns — not
    // one per entity, since a `Channel` is a multiplexed connection, not a per-call one.
    let grpc_backend: Arc<dyn RecordBackend> = Arc::new(
        GrpcBackend::connect(config.grpc_addr.clone(), service_token)
            .await
            .map_err(|e| {
                anyhow::anyhow!(
                    "connecting to upstream '{}' gRPC at {}: {e}",
                    config.name,
                    config.grpc_addr
                )
            })?,
    );

    let entities = response
        .data
        .into_iter()
        .map(|entity| EntityDefinition {
            name: entity.name,
            label: entity.label,
            // Never actually read: this gateway has no `CrudService`/`metap-reconciler` to
            // consult `table_name` against a real database. Just needs to pass
            // `MetadataCompiler::validate`'s `table_name_ok` shape check
            // (`^[a-z][a-z0-9_]*\.[a-z][a-z0-9_]*$`) — the upstream's own real table already
            // backs this entity, this gateway never queries it directly.
            table_name: "metadata.gateway_unused_placeholder".to_string(),
            fields: entity.fields,
            list_views: vec![],
            workflow: entity.workflow,
            unique_constraints: entity.unique_constraints,
            audit: None,
        })
        .collect();

    Ok(UpstreamSchema {
        entities,
        backend: grpc_backend,
    })
}

/// One configured upstream's independently-refreshed state. `last_good` is only ever replaced by
/// a *successful* refresh — a failed one updates `status` alone, so a request arriving while an
/// upstream is down still gets served against that upstream's last-known-good schema/backend
/// (queries against it then fail normally, per-request, exactly as they would have without this
/// cache at all — this only changes what happens to *other* upstreams' entities, and to boot
/// itself).
struct UpstreamCache {
    config: UpstreamConfig,
    http: reqwest::Client,
    secret_store: Arc<dyn metap_control::SecretStore>,
    last_good: ArcSwapOption<UpstreamSchema>,
    status: ArcSwap<UpstreamStatus>,
    // Single-key TTL gate (`try_get_with` de-dupes concurrent misses on the same key, same
    // reason `RegistryCache` uses it) — this crate's own reusable equivalent of
    // `metap_control::RegistryCache`'s pattern, one instance per upstream rather than one shared
    // across tenants. `try_get_with` does not cache an `Err` result (see that method's own
    // docs), so a down upstream is retried on the next refresh rather than waiting out a full TTL
    // window once it recovers.
    gate: Cache<(), Arc<UpstreamSchema>>,
}

impl UpstreamCache {
    fn new(config: UpstreamConfig, http: reqwest::Client, secret_store: Arc<dyn metap_control::SecretStore>) -> Self {
        let status = UpstreamStatus {
            name: config.name.clone(),
            reachable: false,
            error: None,
        };
        Self {
            config,
            http,
            secret_store,
            last_good: ArcSwapOption::empty(),
            status: ArcSwap::new(Arc::new(status)),
            gate: Cache::builder().time_to_live(TTL).build(),
        }
    }

    /// Ensures this upstream's schema is fresh within the TTL window, updating `last_good`/
    /// `status` as a side effect. Never returns anything itself — a caller reads `last_good()`/
    /// `status()` afterward, since a failed refresh has nothing new to hand back anyway.
    async fn refresh(&self) {
        let config = self.config.clone();
        let http = self.http.clone();
        let secret_store = self.secret_store.clone();
        let result = self
            .gate
            .try_get_with((), async move {
                connect_one_upstream(&config, &http, &secret_store).await.map(Arc::new)
            })
            .await;
        match result {
            Ok(schema) => {
                self.last_good.store(Some(schema));
                self.status.store(Arc::new(UpstreamStatus {
                    name: self.config.name.clone(),
                    reachable: true,
                    error: None,
                }));
            }
            Err(e) => {
                tracing::warn!(
                    upstream = self.config.name,
                    error = %e,
                    "upstream schema refresh failed, serving last-known-good state if any"
                );
                self.status.store(Arc::new(UpstreamStatus {
                    name: self.config.name.clone(),
                    reachable: false,
                    error: Some(e.to_string()),
                }));
            }
        }
    }

    fn status(&self) -> Arc<UpstreamStatus> {
        self.status.load_full()
    }

    fn last_good(&self) -> Option<Arc<UpstreamSchema>> {
        self.last_good.load_full()
    }
}

/// Adds the `_gatewayHealth` field every composite schema carries (behind this gateway's own
/// decode-only Bearer auth, so full upstream error text is safe to expose here — unlike
/// `GET /health`, which stays boolean-only for an unauthenticated caller). Returns the whole
/// `GatewayHealth` snapshot as the `Json` scalar, the same "opaque JSON field" mechanism
/// `metap-graphql`'s own `aggregate` field already uses (`json_field_value`'s pattern) — this
/// diagnostic field has no real per-field GraphQL typing to gain from hand-building nested
/// dynamic `Object`/`List` types for it.
fn add_gateway_health_field(mut query: Object, health: GatewayHealth) -> Object {
    query = query.field(Field::new(
        "_gatewayHealth",
        TypeRef::named_nn(JSON_SCALAR),
        move |_ctx| {
            let health = health.clone();
            FieldFuture::new(async move {
                let json = serde_json::to_value(&health).map_err(|e| GqlError::new(e.to_string()))?;
                Ok(GqlValue::from_json(json).ok().map(FieldValue::value))
            })
        },
    ));
    query
}

/// Rebuilds the composite schema from whatever every `UpstreamCache` currently holds (does
/// **not** itself trigger a refresh — the caller, [`GatewaySchemaCache::current`], refreshes
/// every upstream first). Infallible: even in the worst case (every upstream has never once
/// succeeded, or `.finish()` itself somehow errors on a real bug in this crate's own type
/// mapping) this returns a valid, servable schema with zero or few entities plus a health report
/// explaining why — never an `Err` that would take the whole gateway down.
///
/// **Field-dropping rule**: a `Reference` field survives only if its `ref_entity` belongs to some
/// upstream that has succeeded at least once (`known_names`, the union of every currently-good
/// upstream's entity names) — dropping happens *only* for an upstream that has never once
/// succeeded. An upstream that goes down *after* being seen keeps contributing its full
/// last-known-good schema shape (nothing here removes its entities or fields); a live query
/// against it then just gets a normal per-request backend error, unchanged from before this fix.
/// Two upstreams claiming the same entity name in the same cycle: `MetadataRegistry::register`'s
/// own duplicate check rejects the second one, which is logged and dropped for this cycle rather
/// than failing the whole rebuild — the composite schema simply doesn't gain that upstream's
/// version of the name this cycle, same "one failure never kills the whole gateway" principle.
fn build_composite(
    upstream_caches: &[Arc<UpstreamCache>],
    limits: SchemaLimits,
    extend: Option<&(dyn Fn(Object, Object) -> (Object, Object) + Send + Sync)>,
) -> CompositeSchema {
    let snapshots: Vec<(&Arc<UpstreamCache>, Option<Arc<UpstreamSchema>>)> =
        upstream_caches.iter().map(|c| (c, c.last_good())).collect();

    let known_names: HashSet<&str> = snapshots
        .iter()
        .filter_map(|(_, schema)| schema.as_ref())
        .flat_map(|schema| schema.entities.iter().map(|e| e.name.as_str()))
        .collect();

    let mut registry = MetadataRegistry::new();
    let mut by_entity: HashMap<String, Arc<dyn RecordBackend>> = HashMap::new();
    let mut dropped_fields = Vec::new();

    for (cache, schema) in &snapshots {
        let Some(schema) = schema else { continue };
        for entity in &schema.entities {
            let mut entity = entity.clone();
            let entity_name = entity.name.clone();
            entity.fields.retain(|field| {
                if field.kind != FieldKind::Reference {
                    return true;
                }
                match &field.ref_entity {
                    Some(ref_entity) if !known_names.contains(ref_entity.as_str()) => {
                        dropped_fields.push(DroppedFieldReport {
                            entity: entity_name.clone(),
                            field: field.name.clone(),
                            ref_entity: ref_entity.clone(),
                        });
                        false
                    }
                    _ => true,
                }
            });
            if let Err(e) = registry.register(entity) {
                tracing::error!(
                    entity = entity_name,
                    upstream = cache.config.name,
                    error = %e,
                    "dropping this entity for the current refresh cycle: name collision with another upstream"
                );
                continue;
            }
            by_entity.insert(entity_name, schema.backend.clone());
        }
    }

    // Defense in depth: the retain loop above should already have removed every dangling
    // `Reference`, so this is expected to always pass. Logged, not propagated, if it somehow
    // doesn't — see this function's own doc comment for why nothing here is allowed to fail the
    // whole rebuild.
    if let Err(e) = registry.validate_references() {
        tracing::error!(error = %e, "composite registry failed validate_references after field-dropping — this should not happen");
    }

    let entity_count = registry.list_entities().len();
    let backend: Arc<dyn RecordBackend> = Arc::new(CompositeBackend::new(by_entity));
    let health = GatewayHealth {
        upstreams: upstream_caches.iter().map(|c| (*c.status()).clone()).collect(),
        dropped_fields,
    };

    let (builder, query, mutation) = build_schema_parts(&registry, backend.clone(), limits);
    let (query, mutation) = match extend {
        Some(extend) => extend(query, mutation),
        None => (query, mutation),
    };
    let query = add_gateway_health_field(query, health.clone());

    let schema = builder.register(query).register(mutation).finish().unwrap_or_else(|e| {
        tracing::error!(error = %e, "failed to finish composite schema build — serving an empty schema rather than crashing");
        Schema::build("Query", Some("Mutation"), None)
            .register(add_gateway_health_field(Object::new("Query"), health.clone()))
            .register(Object::new("Mutation"))
            .finish()
            .expect("a Query/Mutation pair with only a JSON scalar field is always a valid schema")
    });

    CompositeSchema {
        schema: Arc::new(schema),
        backend,
        entity_count,
        health,
    }
}

/// The TTL-cached, fault-tolerant replacement for what used to be a one-shot boot-time build.
/// Built once by [`build`]/[`build_with_extensions`]; [`current`](Self::current) is what every
/// request (`server.rs`'s `graphql_handler`/`health`/`schema_sdl`) calls to get the schema/
/// backend/health to serve *this* request with — cheap on every call but the first within a TTL
/// window (`gate`'s own `moka::future::Cache`), same "per-request lazy check, no background
/// task" shape `metap_control::RegistryCache` already established for this codebase.
pub struct GatewaySchemaCache {
    upstream_caches: Vec<Arc<UpstreamCache>>,
    limits: SchemaLimits,
    extend: Option<Arc<ExtendFn>>,
    gate: Cache<(), Arc<CompositeSchema>>,
}

impl GatewaySchemaCache {
    /// `Err` only if at least one upstream configured `service_password_secret_ref` (audit 04
    /// B7) and the resulting `SecretStore` backend fails to construct (e.g. `VAULT_ADDR` set but
    /// unreachable) — a deployment that uses only literal `service_password`s never touches this
    /// path and can't fail here. The `SecretStore` construction itself is the one piece of real
    /// I/O this function does; everything else stays lazy until the first `current()` call.
    async fn new(
        upstreams: Vec<UpstreamConfig>,
        limits: SchemaLimits,
        extend: Option<Arc<ExtendFn>>,
    ) -> anyhow::Result<Self> {
        let http = metap_runtime::http_client::default_client();
        let secret_store: Arc<dyn metap_control::SecretStore> =
            if upstreams.iter().any(|u| u.service_password_secret_ref.is_some()) {
                metap_control::build_secret_store(&metap_control::SecretStoreConfig::from_env()).await?
            } else {
                // Never actually called (no upstream references a secret ref) — `EnvStore` is
                // the cheapest concrete value to hold, since it does no I/O to construct, unlike
                // Vault/AWS/GCP.
                Arc::new(metap_control::EnvStore)
            };
        let upstream_caches = upstreams
            .into_iter()
            .map(|config| Arc::new(UpstreamCache::new(config, http.clone(), secret_store.clone())))
            .collect();
        Ok(Self {
            upstream_caches,
            limits,
            extend,
            gate: Cache::builder().time_to_live(TTL).build(),
        })
    }

    /// Refreshes every upstream (cheap no-ops for any still within its own TTL window — see
    /// `UpstreamCache::refresh`) and returns the current composite schema, rebuilding it only
    /// once per TTL window regardless of how many requests call this concurrently (`gate`'s own
    /// `get_with`, infallible — `build_composite` never returns an `Err`).
    pub async fn current(&self) -> Arc<CompositeSchema> {
        futures_util::future::join_all(self.upstream_caches.iter().map(|c| c.refresh())).await;
        let upstream_caches = self.upstream_caches.clone();
        let limits = self.limits;
        let extend = self.extend.clone();
        self.gate
            .get_with((), async move {
                Arc::new(build_composite(&upstream_caches, limits, extend.as_deref()))
            })
            .await
    }
}

/// Builds the standard gateway schema cache, with no fields beyond generic entity CRUD (plus the
/// `_gatewayHealth` diagnostic field every composite schema carries) — see
/// [`build_with_extensions`] for the extension point a caller with its own custom resolvers
/// needs instead. Does no upstream I/O itself — the first real upstream connection happens on the
/// first call to [`GatewaySchemaCache::current`] — except constructing a `SecretStore` when at
/// least one upstream configures `service_password_secret_ref` (audit 04 B7), which is real I/O
/// for the Vault/AWS/GCP backends (never for the literal-password-only default path).
pub async fn build(upstreams: &[UpstreamConfig], limits: SchemaLimits) -> anyhow::Result<GatewaySchemaCache> {
    GatewaySchemaCache::new(upstreams.to_vec(), limits, None).await
}

/// Same as [`build`], but for a caller that needs fields beyond generic entity CRUD — a
/// downstream binary with its own hand-written resolvers for an endpoint no upstream's metadata
/// can describe (e.g. `metap-demo-waf`'s custom REST endpoints — DNS verification, scan dispatch,
/// alert evaluation — none of which are `EntityDefinition` operations `metap-graphql` could
/// synthesize). `extend` receives the assembled `Query`/`Mutation` objects (already carrying every
/// upstream entity's generic `get`/`list`/`create`/`update`/`delete`/`transition` fields) before
/// the schema is finished, and returns them with its own `.field(...)` calls added — see
/// `metap_graphql::build_schema_parts`'s doc comment for why this crate can't add those fields
/// itself (that would be exactly the business-entity knowledge this crate must never carry).
///
/// **`extend` is now `Fn`, not `FnOnce` (audit 04 B1 fix, breaking change)** — the composite
/// schema is rebuilt on every TTL refresh, not once at boot, so this closure is called
/// repeatedly for the life of the process. A caller whose `extend` only reads its own captured
/// state (every real one in this codebase today) is unaffected.
pub async fn build_with_extensions(
    upstreams: &[UpstreamConfig],
    limits: SchemaLimits,
    extend: impl Fn(Object, Object) -> (Object, Object) + Send + Sync + 'static,
) -> anyhow::Result<GatewaySchemaCache> {
    GatewaySchemaCache::new(upstreams.to_vec(), limits, Some(Arc::new(extend))).await
}

#[cfg(test)]
mod tests {
    use super::*;

    fn base_config() -> UpstreamConfig {
        UpstreamConfig {
            name: "test".to_string(),
            grpc_addr: "http://localhost:1".to_string(),
            metadata_url: "http://localhost:1/metadata/entities".to_string(),
            login_url: "http://localhost:1/auth/login".to_string(),
            service_email: "svc@test.local".to_string(),
            service_password: None,
            service_password_secret_ref: None,
        }
    }

    #[tokio::test]
    async fn resolves_the_literal_password_when_no_secret_ref_is_set() {
        let config = UpstreamConfig {
            service_password: Some("literal-password".to_string()),
            ..base_config()
        };
        let password = resolve_service_password(&config, &metap_control::EnvStore)
            .await
            .unwrap();
        assert_eq!(password, "literal-password");
    }

    #[tokio::test]
    async fn resolves_the_password_from_the_secret_store_when_a_secret_ref_is_set() {
        // `EnvStore::get_secret` reads a plain env var named exactly `secret_ref` — real audit 04
        // B7 path, no fake/mock `SecretStore` needed (this backend has zero I/O, matching why
        // `GatewaySchemaCache::new` picks it as the never-called default too).
        std::env::set_var("METAP_GRAPHQL_GATEWAY_TEST_PASSWORD", "rotated-password");
        let config = UpstreamConfig {
            // Deliberately still set, to prove the secret ref wins when both are present.
            service_password: Some("should-not-be-used".to_string()),
            service_password_secret_ref: Some("METAP_GRAPHQL_GATEWAY_TEST_PASSWORD".to_string()),
            ..base_config()
        };
        let password = resolve_service_password(&config, &metap_control::EnvStore)
            .await
            .unwrap();
        assert_eq!(password, "rotated-password");
        std::env::remove_var("METAP_GRAPHQL_GATEWAY_TEST_PASSWORD");
    }

    #[tokio::test]
    async fn errors_clearly_when_neither_password_source_is_configured() {
        let config = base_config();
        let err = resolve_service_password(&config, &metap_control::EnvStore)
            .await
            .unwrap_err();
        assert!(err.to_string().contains("test"), "{err}");
    }
}
