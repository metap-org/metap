//! `MetapApp` — a fluent builder for the HTTP business-service boot sequence, sitting on top of
//! [`crate::bootstrap_platform`]. Found copy-pasted near-identically (2026-09-14 survey,
//! `../../metap-docs/docs/features/36-main-boot-builder.md`) across `templates/metap-app`,
//! `../metap-lowcode`'s `lowcode-admin-api`/`control-api`, and `../metap-demo-waf`'s
//! `zones-service`/`scanning-service`/`alerting-service`: register entities and reconcile their
//! dedicated tables, run the metadata-drift/index-reconcile checks, wire an optional audit sink,
//! an optional JWKS trust root, an optional gRPC transport, then build [`AppState`] (7 positional
//! parameters, easy to transpose by hand) and serve.
//!
//! **Deliberately does not try to cover every binary in this codebase.** Worker loops
//! (`outbox-publisher`, `notification-worker`, `cron-scheduler`) never build an [`AppState`] at
//! all, and don't even agree on *whether* they open a Postgres pool the same way — `notification-
//! worker` opens none, `outbox-publisher`/`cron-scheduler` each connect
//! `config.outbox_database_url()` (which can genuinely differ from `config.database_url`) rather
//! than a fixed URL a shared wrapper could hardcode. The one piece that *is* identical across all
//! of them is the `move || { let url = url.clone(); async move { RabbitEventBus::connect(&url)
//! .await } }` closure, extracted as `metap_infra::rabbitmq_connector` (lives in `metap-infra`,
//! not `metap-runtime`, since it needs `RabbitEventBus` and `metap-runtime` must not depend back
//! on `metap-infra`) — a free function, not a builder type, since there's no multi-step
//! configuration left once that one closure is factored out. Decode-only BFFs (`graphql-gateway`)
//! have no Postgres pool or `AppState` either, and their `main.rs` is already ~30-60 lines — there
//! is no real duplication left to extract there.
//!
//! ## Example
//!
//! ```ignore
//! MetapApp::bootstrap(load_config()?).await?
//!     .with_entities(vec![domain_entity(), zone_entity()]).await?
//!     .with_audit()
//!     .with_jwks_publish().await?
//!     .with_grpc(3001)
//!     .with_extra_routes(routes::router())
//!     .serve()
//!     .await
//! ```

use std::sync::Arc;
use std::time::Duration;

use arc_swap::ArcSwap;
use axum::Router as AxumRouter;
use jsonwebtoken::DecodingKey;
use metap_audit::PostgresAuditTrailStore;
use metap_control::{Router, PLATFORM_TENANT_ID};
use metap_crud::CrudService;
use metap_grpc::OptionalServeConfig;
use metap_http::{build_router, AppState};
use metap_infra::AppConfig;
use metap_jwks::{JwksClient, JwksKeyPair, JwksKeyStore, TokenSigner, TokenVerifier};
use metap_metadata::{EntityDefinition, MetadataRegistry};
use metap_permission::PermissionService;
use sqlx::PgPool;

use crate::{bootstrap_platform, PlatformParts};

// `OptionalServeConfig::token_verifier_override` is typed `Option<Arc<metap_grpc::TokenVerifier>>`,
// which is `pub use metap_jwks::verifier::TokenVerifier` under the hood (see
// `metap-grpc/src/auth.rs`) — the same `TokenVerifier` imported above from `metap_jwks`, so no
// second import/alias is needed here.

enum JwksMode {
    VerifyOnly,
    VerifyAndPublish,
}

struct GrpcConfig {
    default_port: u16,
    auth_context_entity: Option<String>,
}

/// Boot-sequence builder for one HTTP business service. See the module doc comment for what this
/// does and does not cover.
pub struct MetapApp {
    config: AppConfig,
    pool: PgPool,
    router: Router,
    permissions: Arc<PermissionService>,
    decoding_key: DecodingKey,
    private_key_pem: String,
    metadata_base: Arc<MetadataRegistry>,
    metadata: Arc<ArcSwap<MetadataRegistry>>,
    with_audit: bool,
    jwks: Option<JwksMode>,
    grpc: Option<GrpcConfig>,
    extra_routes: AxumRouter<AppState>,
    state_middleware: Option<Box<dyn FnOnce(AxumRouter, AppState) -> AxumRouter>>,
    insecure_cookies: bool,
}

impl MetapApp {
    /// Runs [`crate::bootstrap_platform`] (Postgres pool, tenant `Router`, `PermissionService`,
    /// JWT keypair) and starts with an empty [`MetadataRegistry`] — call [`Self::with_entities`]
    /// or [`Self::with_submitted_entities`] next for a service that owns any entities at all, or
    /// go straight to [`Self::serve`] for one that doesn't (`../metap-lowcode`'s
    /// `lowcode-admin-api`/`control-api`, which serve only hand-written routes).
    pub async fn bootstrap(config: AppConfig) -> anyhow::Result<Self> {
        let PlatformParts {
            pool,
            router,
            permissions,
            decoding_key,
            private_key_pem,
        } = bootstrap_platform(&config).await?;

        let metadata_base = Arc::new(MetadataRegistry::new());
        let metadata = Arc::new(ArcSwap::new(metadata_base.clone()));

        Ok(Self {
            config,
            pool,
            router,
            permissions,
            decoding_key,
            private_key_pem,
            metadata_base,
            metadata,
            with_audit: false,
            jwks: None,
            grpc: None,
            extra_routes: AxumRouter::new(),
            state_middleware: None,
            insecure_cookies: false,
        })
    }

    /// Registers `entities` in the given order (load-bearing — a `Reference` field's FK target
    /// must already be registered, so an entity referencing another must come after it),
    /// validates cross-entity references, reconciles each entity's own dedicated table
    /// (`metap_reconciler::reconcile`, tenant-agnostic DDL against [`PLATFORM_TENANT_ID`] — see
    /// that constant's own doc comment for why that sentinel is correct here), then runs the
    /// metadata-drift and index-reconcile checks against the resulting registry. Use this, not
    /// [`Self::with_submitted_entities`], whenever reconcile order matters — which is every
    /// service with more than one entity, since `submit_entity!`'s auto-discovery order is link
    /// order, not declaration order (see `MetadataRegistry::register_all_submitted`'s own doc
    /// comment).
    pub async fn with_entities(mut self, entities: Vec<EntityDefinition>) -> anyhow::Result<Self> {
        let mut registry = MetadataRegistry::new();
        for entity in &entities {
            registry.register(entity.clone())?;
        }
        registry.validate_references()?;
        let metadata_base = Arc::new(registry);

        for entity in &entities {
            let outcome = metap_reconciler::reconcile(&self.pool, PLATFORM_TENANT_ID, entity, &[]).await?;
            tracing::info!(
                entity = entity.name,
                table = outcome.table,
                ops_applied = outcome.ops_applied,
                "reconciled dedicated table"
            );
        }

        let summaries = metadata_base.list_entities();
        metap_peripherals::check_metadata_drift(&self.pool, &summaries).await;
        metap_peripherals::reconcile_indexes(&self.pool, &summaries).await;

        self.metadata = Arc::new(ArcSwap::new(metadata_base.clone()));
        self.metadata_base = metadata_base;
        Ok(self)
    }

    /// Registers every entity submitted via `submit_entity!` in this binary's dependency graph
    /// (`MetadataRegistry::register_all_submitted`) and validates references — no reconcile, no
    /// drift/index check. Correct only for a service with zero entities of its own (nothing to
    /// reconcile) or whose entities have no FK-dependency order to preserve; every real service
    /// examined while building this (`zones-service` et al.) reconciles in a hand-chosen order
    /// and should use [`Self::with_entities`] instead, even though it also uses `submit_entity!`
    /// for auto-discovery — the two aren't mutually exclusive, this method just doesn't reconcile.
    pub fn with_submitted_entities(mut self) -> anyhow::Result<Self> {
        let mut registry = MetadataRegistry::new();
        registry.register_all_submitted()?;
        registry.validate_references()?;
        let metadata_base = Arc::new(registry);
        self.metadata = Arc::new(ArcSwap::new(metadata_base.clone()));
        self.metadata_base = metadata_base;
        Ok(self)
    }

    /// Wires a `metap-audit` sink (`PostgresAuditTrailStore`, sharing this app's own pool) so
    /// every entity that opts in via its own `EntityDefinition.audit` gets a row in
    /// `metadata.audit_trail_entries` on `create`/`update`/`delete`/`transition`. Deployment-wide
    /// on/off switch — which entities actually opt in is each entity's own declaration, not a
    /// parameter here. Omit this call for `CrudService::new`'s unaudited default.
    pub fn with_audit(mut self) -> Self {
        self.with_audit = true;
        self
    }

    /// Verifies sessions/tokens against the `metap-jwks` Ed25519 trust root (`JWKS_URL`, default
    /// `http://localhost:3000/.well-known/jwks.json`) instead of this app's own static RSA
    /// keypair — for a binary that only ever verifies, never mints for other services. Reads
    /// `JWKS_PRIVATE_KEY_PATH`/`JWKS_KID_PATH` too, since a token this process itself mints
    /// (`POST /auth/login`) should be signed with the same key everything verifies against.
    pub async fn with_jwks(mut self) -> anyhow::Result<Self> {
        self.jwks = Some(JwksMode::VerifyOnly);
        Ok(self)
    }

    /// Same as [`Self::with_jwks`], plus publishes this process's own public key at
    /// `/.well-known/jwks.json` (`metap-jwks-http`) — the one process in a JWKS-trust-root
    /// deployment that other services' `JWKS_URL` points at. Exactly one service per deployment
    /// should call this; the rest call [`Self::with_jwks`].
    pub async fn with_jwks_publish(mut self) -> anyhow::Result<Self> {
        self.jwks = Some(JwksMode::VerifyAndPublish);
        Ok(self)
    }

    /// Opts into gRPC (`GRPC_ENABLED`/`GRPC_PORT` at serve time, via `metap_grpc::optional_serve`
    /// — a no-op unless `GRPC_ENABLED` is set) on `default_port` if the env var is absent.
    pub fn with_grpc(mut self, default_port: u16) -> Self {
        self.grpc = Some(GrpcConfig {
            default_port,
            auth_context_entity: None,
        });
        self
    }

    /// Sets gRPC's `auth_context_entity` (`AuthContext`'s opt-in caller-attributes entity — see
    /// `AppState.auth_context_entity`'s own doc comment). No-op unless [`Self::with_grpc`] was
    /// also called.
    pub fn with_grpc_auth_entity(mut self, entity: impl Into<String>) -> Self {
        if let Some(grpc) = &mut self.grpc {
            grpc.auth_context_entity = Some(entity.into());
        }
        self
    }

    /// Merges `routes` into the router `serve()` builds — the entity-agnostic `/api/:entity*`
    /// core plus whatever this call contributes (a service's own custom routes, an optional
    /// `-http` crate's mount like `metap_graphql_http::router`/`metap_lowcode_http::router`).
    /// Additive across multiple calls, not overwriting — call this once per router you want
    /// mounted.
    pub fn with_extra_routes(mut self, routes: AxumRouter<AppState>) -> Self {
        self.extra_routes = self.extra_routes.merge(routes);
        self
    }

    /// Escape hatch for middleware that needs a real `AppState` value (`axum::middleware::
    /// from_fn_with_state`) — `AppState` doesn't exist until [`Self::serve`] builds it, so this
    /// can't be expressed as a plain `axum::Router<AppState>` like [`Self::with_extra_routes`].
    /// `f` runs once, right after the router is built, with the just-built `axum::Router` and a
    /// clone of the `AppState` that built it (same "clone `state` before it's consumed into
    /// `build_router`" shape `zones-service`'s own `zone_delete_guard`/`zone_domain_guard` wiring
    /// used by hand). Most services need nothing here — only one of the 6 examined while building
    /// this did.
    pub fn with_state_middleware(mut self, f: impl FnOnce(AxumRouter, AppState) -> AxumRouter + 'static) -> Self {
        self.state_middleware = Some(Box::new(f));
        self
    }

    /// Sets `AppState.cookie_secure = false` — for a dev binary serving plain `http://localhost`,
    /// where a `Secure` session cookie (the correct default for any real HTTPS deployment) is
    /// silently dropped by the browser. See `AppState.cookie_secure`'s own doc comment.
    pub fn insecure_cookies(mut self) -> Self {
        self.insecure_cookies = true;
        self
    }

    /// Builds `AppState`, applies every opt-in configured above, binds `{config.host}:
    /// {config.port}`, and serves until Ctrl+C/SIGTERM (`metap_runtime::serve::run`). Consumes
    /// `self` — this is the terminal call.
    pub async fn serve(self) -> anyhow::Result<()> {
        let mut state = AppState::new(
            self.pool,
            self.metadata_base,
            self.metadata,
            self.permissions,
            self.decoding_key,
            self.private_key_pem,
            self.router,
        );

        if self.with_audit {
            state.crud = Arc::new(CrudService::with_audit(
                state.router.clone(),
                state.metadata.clone(),
                state.permissions.clone(),
                Arc::new(PostgresAuditTrailStore::new(state.pool.clone())),
            ));
        }

        if self.insecure_cookies {
            state.cookie_secure = false;
        }

        let mut jwks_key_store: Option<Arc<tokio::sync::RwLock<JwksKeyStore>>> = None;
        if let Some(mode) = &self.jwks {
            let private_key_path = std::env::var("JWKS_PRIVATE_KEY_PATH").map_err(|_| {
                anyhow::anyhow!("JWKS_PRIVATE_KEY_PATH must be set to use with_jwks()/with_jwks_publish()")
            })?;
            let kid_path = std::env::var("JWKS_KID_PATH")
                .map_err(|_| anyhow::anyhow!("JWKS_KID_PATH must be set to use with_jwks()/with_jwks_publish()"))?;
            let kid = std::fs::read_to_string(&kid_path)?.trim().to_string();
            let private_pkcs8 = std::fs::read(&private_key_path)?;

            let signing_key = JwksKeyPair::from_pkcs8(kid.clone(), private_pkcs8.clone())?;
            state.token_signer = Some(Arc::new(TokenSigner::Jwks {
                key: Arc::new(signing_key),
            }));

            let jwks_url =
                metap_runtime::env::env_or("JWKS_URL", "http://localhost:3000/.well-known/jwks.json".to_string());
            state.token_verifier = Some(Arc::new(TokenVerifier::Jwks {
                client: Arc::new(JwksClient::new(jwks_url, Duration::from_secs(300))),
                leeway: 20,
            }));

            if matches!(mode, JwksMode::VerifyAndPublish) {
                let published_key = JwksKeyPair::from_pkcs8(kid, private_pkcs8)?;
                jwks_key_store = Some(Arc::new(tokio::sync::RwLock::new(JwksKeyStore::new(published_key))));
            }
        }

        let grpc_handle = match &self.grpc {
            Some(grpc) => {
                metap_grpc::optional_serve(
                    &self.config.host,
                    grpc.default_port,
                    OptionalServeConfig {
                        crud: state.crud.clone(),
                        router: state.router.clone(),
                        jwt_decoding_key: state.jwt_decoding_key.clone(),
                        auth_context_entity: grpc.auth_context_entity.clone(),
                        metadata: state.metadata.clone(),
                        context_attributes_cache: state.context_attributes_cache.clone(),
                        token_verifier_override: state.token_verifier.clone(),
                    },
                )
                .await?
            }
            None => None,
        };

        state.config.reload().await?;

        let addr = format!("{}:{}", self.config.host, self.config.port);
        let middleware = self.state_middleware;
        let state_for_middleware = state.clone();
        let mut router = build_router(state, &self.config.cors_origins, self.extra_routes);

        if let Some(jwks_key_store) = jwks_key_store {
            // `fallback_service`, not `route_service`/`nest_service` — axum 0.8 refuses both for
            // mounting a whole `Router`-typed service at/under this router's own root. Safe here
            // because `build_router`'s own output never sets its own fallback: this router tries
            // its normal routes first, and only an unmatched request reaches
            // `metap_jwks_http::router`'s single registered path.
            router = router.fallback_service(metap_jwks_http::router(jwks_key_store));
        }

        if let Some(middleware) = middleware {
            router = middleware(router, state_for_middleware);
        }

        tracing::info!(%addr, "listening");
        metap_runtime::serve::run(
            &addr,
            router.into_make_service_with_connect_info::<std::net::SocketAddr>(),
        )
        .await?;

        // Deliberately not joined — no in-flight-publish state to drain, and `metap_grpc::serve`
        // has no shutdown-signal parameter to wire one in with. Dropping a still-running task
        // here is fine: the process is exiting anyway.
        drop(grpc_handle);

        Ok(())
    }
}
