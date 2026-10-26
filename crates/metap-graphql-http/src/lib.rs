//! Mounts `metap-graphql`'s dynamic schema at `POST /graphql` — same "own crate, zero dependency
//! from `metap-http`" shape as `metap-control-http`/`metap-lowcode-http`: `metap-http` has no
//! idea this crate exists, a binary that wants a GraphQL endpoint merges [`router`]'s output into
//! `metap_http::build_router`'s `extra_routes` argument itself (see `../metap-demo-crm/src/main.rs`
//! for the pattern this follows). Auth reuses `metap_http::auth::AuthContext` directly — the
//! exact same JWT verification REST already runs — so GraphQL and REST can't drift on who's
//! allowed to call what; the resulting `RequestContext` is attached to every GraphQL request via
//! `metap_graphql::with_request_data`.
//!
//! **Schema hot-reload.** A low-code entity publish/rollback swaps `AppState.metadata`'s
//! `ArcSwap<MetadataRegistry>` to a new `Arc` (`metap-lowcode-http`'s `apply_registry`) — this
//! crate must not keep serving a schema built from a stale registry indefinitely. Rather than a
//! background poller, `SchemaHolder` rebuilds lazily: each request compares the `Arc` it last
//! built from against the current one (`Arc::ptr_eq` — a new publish always installs a genuinely
//! new `Arc`, cheap to detect), rebuilding only when they differ. A binary that never touches
//! `metap-lowcode-http` never publishes a new registry, so this rebuild path is simply never
//! exercised for it — no forced coupling between the two optional crates.

use std::collections::HashMap;
use std::sync::Arc;

use async_graphql::http::GraphiQLSource;
use async_graphql_axum::{GraphQLRequest, GraphQLResponse};
use axum::extract::State;
use axum::http::header::CONTENT_SECURITY_POLICY;
use axum::http::HeaderValue;
use axum::response::{Html, IntoResponse};
use axum::routing::{get, post};
use axum::Router;
use metap_crud::RecordBackend;
use metap_graphql::{build_schema_parts, build_schema_parts_with_federation, with_request_data, Schema, SchemaLimits};
use metap_http::auth::AuthContext;
use metap_http::AppState;
use metap_metadata::MetadataRegistry;
use tokio::sync::Mutex;
use uuid::Uuid;

mod platform_fields;
pub mod public_schema;

/// A cached, already-built schema plus the registry `Arc` it was built from — compared by
/// `Arc::ptr_eq` to detect staleness, never by value.
type CachedSchema = (Arc<MetadataRegistry>, Arc<Schema>);

struct SchemaHolder {
    backend: Arc<dyn RecordBackend>,
    limits: SchemaLimits,
    /// Whether this holder builds/rebuilds with Apollo Federation support (`@key`/`_entities`) —
    /// see `metap_graphql::build_schema_with_federation`'s doc comment. Fixed for the holder's
    /// lifetime, same as `backend`/`limits` — a binary either mounts a federation-capable
    /// `/graphql` or it doesn't, this never toggles mid-flight.
    federation: bool,
    /// Schema-wide data every `platform_fields` resolver reads (`state.pool`/`state.router`/
    /// `state.permissions`/`state.config`/`state.context_attributes_cache`) — safe to hold for
    /// the schema's lifetime like `backend` above, since none of those fields hot-swap the way
    /// `state.crud`/`state.metadata` do (only those two are re-read fresh per request, via
    /// [`router_with_federation`]'s own `State<AppState>` extractor).
    state: AppState,
    /// Keyed by tenant (`docs/features/15-tenant-scoped-lowcode-metadata.md`) — a tenant-scoped
    /// `metadata_resolver` (`AppState::metadata_for`) can hand back a *different* registry per
    /// tenant, so one global `(registry, schema)` pair is no longer enough: each tenant gets its
    /// own cached pair, rebuilt lazily the same way the single global cache used to (`Arc::ptr_eq`
    /// against the registry this tenant last built from), just keyed per tenant now. When no
    /// resolver is configured, `metadata_for` always returns the same global `Arc` for every
    /// tenant, so every tenant's entry converges on one identical cached schema anyway — this
    /// costs one extra `HashMap` lookup per request in that case, not a rebuild.
    tenant_cache: Mutex<HashMap<Uuid, CachedSchema>>,
    /// The tenant-agnostic schema `GET /graphql/schema.graphql` serves — that route is
    /// unauthenticated (see its own doc comment), so there is no tenant to resolve by; this
    /// always builds straight from `state.metadata`'s plain snapshot, deliberately bypassing
    /// `metadata_resolver` even when one is configured (same "public surface describes shape
    /// only, stays global" precedent as `GET /metadata/openapi.json`).
    global_cache: Mutex<CachedSchema>,
}

impl SchemaHolder {
    fn new(
        metadata: Arc<MetadataRegistry>,
        backend: Arc<dyn RecordBackend>,
        limits: SchemaLimits,
        federation: bool,
        state: AppState,
    ) -> anyhow::Result<Self> {
        let schema = Arc::new(Self::build(
            &metadata,
            backend.clone(),
            limits,
            federation,
            state.clone(),
        )?);
        Ok(Self {
            backend,
            limits,
            federation,
            state,
            tenant_cache: Mutex::new(HashMap::new()),
            global_cache: Mutex::new((metadata, schema)),
        })
    }

    /// Builds the entity schema via `build_schema_parts`/`build_schema_parts_with_federation`
    /// (stopping short of `.finish()`, exactly the extension point their own doc comments
    /// describe), adds every hand-written non-entity field (`crate::platform_fields`) onto the
    /// same `query`/`mutation` objects, attaches `state` as schema-wide data alongside `backend`,
    /// then finishes.
    fn build(
        metadata: &MetadataRegistry,
        backend: Arc<dyn RecordBackend>,
        limits: SchemaLimits,
        federation: bool,
        state: AppState,
    ) -> Result<Schema, async_graphql::dynamic::SchemaError> {
        let (builder, query, mutation) = if federation {
            build_schema_parts_with_federation(metadata, backend, limits)
        } else {
            build_schema_parts(metadata, backend, limits)
        };
        let (builder, query, mutation) = platform_fields::add_platform_fields(builder, query, mutation);
        builder.data(state).register(query).register(mutation).finish()
    }

    /// Returns the schema `tenant_id` should see, resolving its registry via
    /// `state.metadata_for` and rebuilding only when that tenant's resolved registry `Arc` has
    /// changed since this holder last built for it (same `Arc::ptr_eq` staleness check the old
    /// single global cache used, just keyed per tenant now). A failed rebuild on a tenant's
    /// *first* request propagates (there is no "last good schema" to fall back to yet); a failed
    /// rebuild on a later request keeps serving that tenant's last good schema instead, same
    /// resilience the old global cache had.
    async fn get_for_tenant(&self, tenant_id: Uuid) -> anyhow::Result<Arc<Schema>> {
        let current = self.state.metadata_for(tenant_id).await?;
        let mut guard = self.tenant_cache.lock().await;
        let stale = match guard.get(&tenant_id) {
            Some((cached, _)) => !Arc::ptr_eq(cached, &current),
            None => true,
        };
        if stale {
            if let Ok(schema) = Self::build(
                &current,
                self.backend.clone(),
                self.limits,
                self.federation,
                self.state.clone(),
            ) {
                guard.insert(tenant_id, (current.clone(), Arc::new(schema)));
            }
        }
        guard
            .get(&tenant_id)
            .map(|(_, schema)| schema.clone())
            .ok_or_else(|| anyhow::anyhow!("failed to build GraphQL schema for tenant {tenant_id}"))
    }

    /// The tenant-agnostic schema `/graphql/schema.graphql` serves — see `global_cache`'s doc
    /// comment. Same resilience as before this feature: a failed rebuild keeps serving the last
    /// good global schema rather than erroring.
    async fn get_global(&self) -> Arc<Schema> {
        let current = self.state.metadata.load_full();
        let mut guard = self.global_cache.lock().await;
        if !Arc::ptr_eq(&guard.0, &current) {
            if let Ok(schema) = Self::build(
                &current,
                self.backend.clone(),
                self.limits,
                self.federation,
                self.state.clone(),
            ) {
                *guard = (current, Arc::new(schema));
            }
        }
        guard.1.clone()
    }
}

/// Builds the `POST /graphql` route, plus `GET /graphql/schema.graphql` — the live schema's SDL
/// as plain text, the GraphQL counterpart to `GET /metadata/openapi.json`. A newcomer/FE dev
/// used to a traditional code-first `schema.gql` file has nothing to read here otherwise, since
/// this schema is built at runtime from `MetadataRegistry` (`metap-graphql/src/schema.rs`'s doc
/// comment — no per-entity compile-time types exist to generate a static one from). GraphiQL's
/// own "Docs" panel ([`playground_router`]'s doc comment) already covers *interactive*
/// exploration against a live server; this route is for the complementary case — a checked-in
/// `schema.graphql` file a dev can grep/read without running anything, regenerated by a plain
/// `curl`/`fetch` script the same way `platform-ui`'s `generate:types` already works for REST
/// (see that package's `generate:graphql-schema` script). Unauthenticated, matching
/// `openapi_json`'s own precedent (`../metap-http/src/routes/metadata.rs`'s doc comment) — it
/// describes API shape (entity/field names via the schema's dynamic type names), never tenant
/// data, so requiring a minted token here would only get in the way of the codegen use case this
/// exists for. Shares `SchemaHolder` with `/graphql` itself, so it always reflects the same
/// hot-reloaded schema a real query would run against, never a stale snapshot.
///
/// `state` is only read here to seed the initial schema —
/// every subsequent request re-reads `AppState` fresh via its own `State<AppState>` extractor
/// (so a hot-swapped `metadata`/replaced `crud` is always picked up), this constructor argument
/// doesn't get held onto. `state.crud` is `Arc<CrudService>`, coerced to `Arc<dyn RecordBackend>`
/// here — the single-service (in-process) case of the same seam the BFF gateway
/// (`crates/metap-graphql-gateway`) uses a remote `GrpcBackend`/`CompositeBackend` for instead.
pub fn router(state: &AppState, limits: SchemaLimits) -> anyhow::Result<Router<AppState>> {
    router_with_federation(state, limits, false)
}

/// Same as [`router`], with Apollo Federation v2 subgraph support opt-in via `federation` — see
/// `metap_graphql::build_schema_with_federation`'s doc comment for what that adds (`@key`/
/// `_entities`) and `../metap-docs/docs/roadmap/91-graphql-federation-capability.md` for the
/// full design. A binary decides this itself (typically
/// `metap_runtime::env::flag_enabled("GRAPHQL_FEDERATION_ENABLED")` read in its own `main.rs`,
/// same opt-in-via-env-var shape as `GRPC_ENABLED`) — this crate has no opinion on how the
/// decision is made, only on how to build the schema once it is. **No binary in this
/// organization's repos passes `true` here yet** — the capability exists, nothing has turned it
/// on.
pub fn router_with_federation(
    state: &AppState,
    limits: SchemaLimits,
    federation: bool,
) -> anyhow::Result<Router<AppState>> {
    let initial_metadata = state.metadata.load_full();
    let initial_backend: Arc<dyn RecordBackend> = state.crud.clone();
    let holder = Arc::new(SchemaHolder::new(
        initial_metadata,
        initial_backend,
        limits,
        federation,
        state.clone(),
    )?);
    let sdl_holder = holder.clone();

    Ok(Router::new()
        .route(
            "/graphql",
            post(
                move |State(state): State<AppState>, AuthContext(context): AuthContext, req: GraphQLRequest| {
                    let holder = holder.clone();
                    async move {
                        let tenant_id = match Uuid::parse_str(&context.tenant_id) {
                            Ok(id) => id,
                            Err(e) => {
                                return GraphQLResponse::from(async_graphql::Response::from_errors(vec![
                                    async_graphql::ServerError::new(
                                        format!("invalid tenant id on request context: {e}"),
                                        None,
                                    ),
                                ]));
                            }
                        };
                        let schema = match holder.get_for_tenant(tenant_id).await {
                            Ok(schema) => schema,
                            Err(e) => {
                                return GraphQLResponse::from(async_graphql::Response::from_errors(vec![
                                    async_graphql::ServerError::new(e.to_string(), None),
                                ]));
                            }
                        };
                        let backend: Arc<dyn RecordBackend> = state.crud.clone();
                        let request = with_request_data(req.into_inner(), backend, context);
                        GraphQLResponse::from(schema.execute(request).await)
                    }
                },
            ),
        )
        .route(
            "/graphql/schema.graphql",
            get(move |State(_state): State<AppState>| {
                let holder = sdl_holder.clone();
                async move { holder.get_global().await.sdl() }
            }),
        ))
}

/// GraphiQL's own hosted build (`GraphiQLSource::build()`'s generated HTML) loads React/GraphiQL
/// from `unpkg.com` and runs an inline `<script>` to boot it — `metap-http`'s global
/// `security_headers` middleware's default CSP (`script-src 'self'`, no `unsafe-inline`) blocks
/// every one of those by design (it's tuned for the JSON API + a same-origin SPA, not a page
/// that intentionally loads third-party script). Rather than loosen that default for every
/// route, this handler sets its *own* `content-security-policy` header before the shared
/// middleware runs; `security_headers` uses `HeaderMap::entry(...).or_insert_with(...)` for that
/// header specifically so a route that already set one (this one) keeps it instead of being
/// overwritten.
const PLAYGROUND_CSP: &str = "default-src 'self'; script-src 'self' https://unpkg.com 'unsafe-inline'; \
                               style-src 'self' https://unpkg.com 'unsafe-inline'; \
                               img-src 'self' data: https://graphql.org; font-src 'self' https: data:; \
                               connect-src 'self'";

/// `GET /graphql/playground` — a GraphiQL page wired at `endpoint` (typically `/graphql`, the
/// path [`router`] mounts). Unauthenticated (it's just static HTML with no data in it — the
/// actual queries it sends still go through the real, auth-gated `/graphql` POST endpoint,
/// where a caller pastes their own bearer token into GraphiQL's own header editor panel), so
/// callers should gate mounting this behind their own non-production check rather than this
/// crate hardcoding one (e.g. `AppConfig.node_env != NodeEnv::Production`) — see
/// `../metap-demo-jira/src/main.rs` for the pattern. GraphiQL's own "Docs" panel (reads the
/// schema via GraphQL introspection, which this crate's schema doesn't disable) is this
/// platform's closest equivalent to Swagger UI for REST: live, always in sync with the actual
/// schema, no separate spec file to keep updated by hand.
///
/// Generic over `S` rather than fixed to `AppState`: this handler never touches `AppState`'s
/// fields (Postgres pool, `CrudService`, etc.) at all — it only serves static HTML — so a binary
/// with no `AppState` (the BFF gateway, `crates/metap-graphql-gateway`, which has no Postgres/CrudService
/// of its own) can merge this router into its own `axum::Router<GatewayState>` unchanged instead
/// of reimplementing the same handler.
pub fn playground_router<S: Clone + Send + Sync + 'static>(endpoint: &str) -> Router<S> {
    let html = GraphiQLSource::build().endpoint(endpoint).finish();
    Router::new().route(
        "/graphql/playground",
        get(move || {
            let html = html.clone();
            async move {
                let mut response = Html(html).into_response();
                response
                    .headers_mut()
                    .insert(CONTENT_SECURITY_POLICY, HeaderValue::from_static(PLAYGROUND_CSP));
                response
            }
        }),
    )
}
