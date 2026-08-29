//! Mounts `metap-graphql`'s dynamic schema at `POST /graphql` — same "own crate, zero dependency
//! from `metap-http`" shape as `metap-control-http`/`metap-lowcode-http`: `metap-http` has no
//! idea this crate exists, a binary that wants a GraphQL endpoint merges [`router`]'s output into
//! `metap_http::build_router`'s `extra_routes` argument itself (see `apps/crm-server/src/main.rs`
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
use metap_graphql::{build_schema, with_request_data, Schema, SchemaLimits};
use metap_http::auth::AuthContext;
use metap_http::AppState;
use metap_metadata::MetadataRegistry;
use tokio::sync::Mutex;

struct SchemaHolder {
    backend: Arc<dyn RecordBackend>,
    limits: SchemaLimits,
    cached: Mutex<(Arc<MetadataRegistry>, Arc<Schema>)>,
}

impl SchemaHolder {
    fn new(
        metadata: Arc<MetadataRegistry>,
        backend: Arc<dyn RecordBackend>,
        limits: SchemaLimits,
    ) -> anyhow::Result<Self> {
        let schema = Arc::new(build_schema(&metadata, backend.clone(), limits)?);
        Ok(Self {
            backend,
            limits,
            cached: Mutex::new((metadata, schema)),
        })
    }

    /// Returns the current schema, rebuilding first if `current_metadata` isn't the same `Arc`
    /// this holder last built from. A failed rebuild (should only happen if a hot-swapped
    /// registry is somehow invalid, which `MetadataRegistry::register`/`validate_references`
    /// should already have rejected before it was ever published) keeps serving the last good
    /// schema rather than erroring every subsequent request.
    async fn get(&self, current_metadata: &Arc<MetadataRegistry>) -> Arc<Schema> {
        let mut guard = self.cached.lock().await;
        if !Arc::ptr_eq(&guard.0, current_metadata) {
            if let Ok(schema) = build_schema(current_metadata, self.backend.clone(), self.limits) {
                *guard = (current_metadata.clone(), Arc::new(schema));
            }
        }
        guard.1.clone()
    }
}

/// Builds the `POST /graphql` route. `state` is only read here to seed the initial schema —
/// every subsequent request re-reads `AppState` fresh via its own `State<AppState>` extractor
/// (so a hot-swapped `metadata`/replaced `crud` is always picked up), this constructor argument
/// doesn't get held onto. `state.crud` is `Arc<CrudService>`, coerced to `Arc<dyn RecordBackend>`
/// here — the single-service (in-process) case of the same seam the BFF gateway
/// (`crates/graphql-gateway`) uses a remote `GrpcBackend`/`CompositeBackend` for instead.
pub fn router(state: &AppState, limits: SchemaLimits) -> anyhow::Result<Router<AppState>> {
    let initial_metadata = state.metadata.load_full();
    let initial_backend: Arc<dyn RecordBackend> = state.crud.clone();
    let holder = Arc::new(SchemaHolder::new(initial_metadata, initial_backend, limits)?);

    Ok(Router::new().route(
        "/graphql",
        post(
            move |State(state): State<AppState>, AuthContext(context): AuthContext, req: GraphQLRequest| {
                let holder = holder.clone();
                async move {
                    let current_metadata: Arc<MetadataRegistry> = state.metadata.load_full();
                    let schema = holder.get(&current_metadata).await;
                    let backend: Arc<dyn RecordBackend> = state.crud.clone();
                    let request = with_request_data(req.into_inner(), backend, context);
                    GraphQLResponse::from(schema.execute(request).await)
                }
            },
        ),
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
/// `apps/jira-server/src/main.rs` for the pattern. GraphiQL's own "Docs" panel (reads the
/// schema via GraphQL introspection, which this crate's schema doesn't disable) is this
/// platform's closest equivalent to Swagger UI for REST: live, always in sync with the actual
/// schema, no separate spec file to keep updated by hand.
///
/// Generic over `S` rather than fixed to `AppState`: this handler never touches `AppState`'s
/// fields (Postgres pool, `CrudService`, etc.) at all — it only serves static HTML — so a binary
/// with no `AppState` (the BFF gateway, `crates/graphql-gateway`, which has no Postgres/CrudService
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
