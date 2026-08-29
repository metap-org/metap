//! The boot sequence the old `apps/crm/src/main.ts` + `app.ts`'s `buildApp` used to
//! document (register entities, validate references, drift check, index reconcile, serve)
//! reassembled from the `metap` facade crate (see `crates/metap/src/lib.rs` — a thin
//! re-export layer over `crates/metap-*`, so this file only needs one dependency/import
//! instead of naming each sub-crate). Run from this crate's own directory
//! (`apps/crm-server/`) so `.env`/`keys/` resolution works — `pnpm dev:rs` does this via
//! `cd`; see `metap-infra/src/config.rs` for the `.env` resolution itself.
//!
//! `apps/crm-server`/`apps/crm-fe` together are the demo/test app for this whole project, not
//! a real product — see `docs/features/README.md`. `src/entities/` holds every entity this
//! demo app registers, kept in one folder since none of them are a real business module (see
//! each entity's own `docs/features/*.md` brief for what it was built to prove).

mod entities;

use entities::{customer_entity, inventory_movement_entity, journal_entry_entity, sales_order_entity};

use std::sync::Arc;

use arc_swap::ArcSwap;
use jsonwebtoken::DecodingKey;
use metap::http::cache::ContextAttributesCache;
use metap::infra::RabbitEventBus;
use metap::prelude::*;
use tower_http::services::{ServeDir, ServeFile};

#[tokio::main]
async fn main() -> anyhow::Result<()> {
    metap::infra::init_tracing();
    let config = load_config()?;

    tracing::info!("connecting to postgres...");
    let pool = connect_db(&config.database_url).await?;

    // Code-authored entities only — fixed for the process lifetime, never touched by a
    // DB-authored publish/rollback (`docs/roadmap.md` Phase 11 / Phase A). Kept separate
    // from the merged runtime registry below so `metap-http`'s admin routes can reject a
    // DB-authored draft whose name collides with one of these, without themselves knowing
    // any entity name.
    let mut code_registry = MetadataRegistry::new();
    code_registry.register(customer_entity::customer_entity())?;
    code_registry.register(sales_order_entity::sales_order_entity())?;
    code_registry.register(inventory_movement_entity::inventory_movement_entity())?;
    code_registry.register(journal_entry_entity::journal_entry_entity())?;
    code_registry.validate_references()?;
    let metadata_base = Arc::new(code_registry);

    // Table-per-entity for `crm.customers` (`docs/roadmap/36-crm-server-table-per-entity.md`) —
    // same mechanism `apps/jira-server`'s boot sequence already uses, applied here for the
    // first time to a `Schema`-strategy tenant's *shared* platform pool rather than a
    // `DedicatedDb` tenant's own one. The dedicated table this reconciles is genuinely shared
    // across every schema-tenant on this pool (same as `records` already is) — the fixed
    // `tenant_id` below is only bookkeeping for `reconcile()`'s advisory-lock/introspection
    // calls, not a scope on the table itself; safe here specifically because this call happens
    // once at boot, never concurrently from multiple tenants the way a real multi-tenant
    // orchestrator would run it.
    let default_tenant_id: uuid::Uuid = "00000000-0000-0000-0000-000000000001".parse().unwrap();
    let customer_reconcile =
        metap_reconciler::reconcile(&pool, default_tenant_id, &customer_entity::customer_entity(), &[]).await?;
    tracing::info!(
        entity = "crm.customers",
        table = customer_reconcile.table,
        ops_applied = customer_reconcile.ops_applied,
        "reconciled dedicated table"
    );

    // `sales.orders` -> `inventory.movements` -> `accounting.journal`
    // (`docs/roadmap/45-sales-inventory-journal-table-per-entity.md`) — the 3 remaining
    // code-authored entities, moved after `crm.customers` above. Order is load-bearing: each
    // entity's Reference field FKs into the previous one's dedicated table
    // (`customer`->`crm.customers`, `referenceOrder`->`sales.orders`,
    // `referenceMovement`->`inventory.movements`), and `reconcile()` always builds that FK
    // against `qualified_table_name_for(ref_entity)` regardless of whether the target has
    // been reconciled yet — so the target's table must already exist, same reasoning
    // `apps/jira-server`'s `project_entity.rs`/`sprint_entity.rs` document.
    let sales_order_reconcile =
        metap_reconciler::reconcile(&pool, default_tenant_id, &sales_order_entity::sales_order_entity(), &[]).await?;
    tracing::info!(
        entity = "sales.orders",
        table = sales_order_reconcile.table,
        ops_applied = sales_order_reconcile.ops_applied,
        "reconciled dedicated table"
    );
    let inventory_movement_reconcile = metap_reconciler::reconcile(
        &pool,
        default_tenant_id,
        &inventory_movement_entity::inventory_movement_entity(),
        &[],
    )
    .await?;
    tracing::info!(
        entity = "inventory.movements",
        table = inventory_movement_reconcile.table,
        ops_applied = inventory_movement_reconcile.ops_applied,
        "reconciled dedicated table"
    );
    let journal_entry_reconcile = metap_reconciler::reconcile(
        &pool,
        default_tenant_id,
        &journal_entry_entity::journal_entry_entity(),
        &[],
    )
    .await?;
    tracing::info!(
        entity = "accounting.journal",
        table = journal_entry_reconcile.table,
        ops_applied = journal_entry_reconcile.ops_applied,
        "reconciled dedicated table"
    );

    // DB-authored entities (`metap-lowcode`, Phase A sub-project 1/2) — every *enabled*
    // entity that has been published at least once, merged on top of the code-authored base
    // (a disabled entity stays out of the registry entirely, same as if it had never been
    // published — see `metap_lowcode::list_enabled_published`'s doc comment). Empty on a
    // fresh install; `metap-lowcode-http`'s publish/rollback/enable-toggle handlers rebuild
    // and swap this same way at runtime, so a restart is never required to pick up a change.
    let db_entities: Vec<_> = metap::lowcode::list_enabled_published(&pool)
        .await?
        .into_iter()
        .map(|(_, def)| def.to_entity_definition())
        .collect();
    let runtime_registry = metadata_base.merge_with(db_entities)?;
    // `merge_with`/`register` only run per-entity shape validation — cross-entity reference
    // checks (a DB-authored `refEntity` pointing at a code-authored entity, or vice versa)
    // need the merged registry, not `code_registry` alone, so this can't be skipped just
    // because `code_registry.validate_references()` already ran above.
    runtime_registry.validate_references()?;

    let entities = runtime_registry.list_entities();
    check_metadata_drift(&pool, &entities).await;
    reconcile_indexes(&pool, &entities).await;

    let metadata = Arc::new(ArcSwap::new(Arc::new(runtime_registry)));

    // Which `SecretStore` resolves a `DedicatedDb` tenant's DSN — decided here, not inside
    // `AppState::new`, same "wiring inline at the composition root" pattern as everything else
    // in this file. `metap::control::build_secret_store` picks `EnvStore` (unchanged default)
    // unless one of `GCP_SECRETS_PROJECT_ID`/`AWS_SECRETS_REGION`/`VAULT_ADDR` is configured
    // (`docs/roadmap.md` Phase 8/16) — opt-in, no downstream project is forced to run a Vault
    // container or hold cloud credentials to develop normally. See that function's own doc
    // comment for the exact precedence order when more than one is somehow set.
    let secret_store = metap::control::build_secret_store(&config).await?;

    // Built once here, not inside `AppState::new`, and shared with `PostgresPolicyStore` below
    // (`docs/roadmap.md` Phase 16 gap, closed 2026-08-20 — role lookup and RBAC/policy storage
    // now route through this same `Router` instead of a fixed pool, so a `DedicatedDb`-strategy
    // tenant's `users`/`user_roles`/`policies` are reached correctly). Sharing one instance
    // means one `RegistryCache`, not two independently-warming caches for the same
    // `control.tenants` lookups.
    let tenant_registry = Arc::new(metap::control::PostgresTenantRegistry::new(pool.clone()));
    let router = metap::control::Router::new(
        pool.clone(),
        metap::control::RegistryCache::new(tenant_registry),
        secret_store,
    );

    // Opt-in distributed policy cache (`POLICY_CACHE_REDIS_URL`) — unset stays fully uncached,
    // exactly as before this existed (see `metap::cache`'s doc comment for why Redis/DragonflyDB
    // rather than the in-process `MokaCache`: policy writes must be visible to every server
    // instance behind a load balancer, not just the one that served the write).
    let permissions = match &config.policy_cache_redis_url {
        Some(url) => {
            let ttl = std::time::Duration::from_secs(config.policy_cache_ttl_seconds);
            let cache = metap::cache::RedisCache::connect(url, ttl).await?;
            PermissionService::with_cache(
                Box::new(PostgresPolicyStore::new(router.clone())),
                Arc::new(cache) as Arc<dyn metap::cache::Cache>,
            )
        }
        None => PermissionService::new(Box::new(PostgresPolicyStore::new(router.clone()))),
    };

    let public_key_pem = std::fs::read(&config.auth_jwt_public_key_path)
        .map_err(|e| anyhow::anyhow!("failed to read {}: {e}", config.auth_jwt_public_key_path))?;
    let decoding_key = DecodingKey::from_rsa_pem(&public_key_pem)?;

    // Needed only for POST /auth/login (metap_peripherals::mint_jwt) — crm-server issues
    // tokens now, not just verifies them, so both halves of the keypair are load-bearing.
    let private_key_pem = std::fs::read_to_string(&config.auth_jwt_private_key_path)
        .map_err(|e| anyhow::anyhow!("failed to read {}: {e}", config.auth_jwt_private_key_path))?;

    let mut state = AppState::new(
        pool,
        metadata_base,
        metadata,
        Arc::new(permissions),
        decoding_key,
        private_key_pem,
        router,
    );
    // `AUTH_CONTEXT_ENTITY` opt-in (`docs/features/03-organization-identity.md`) — `AppState::new`
    // leaves both fields at their no-op defaults; the composition root assigns them here since
    // most deployments never set this env var and don't need it threaded through the constructor.
    state.auth_context_entity = config.auth_context_entity.as_deref().map(Arc::from);
    state.context_attributes_cache =
        ContextAttributesCache::new(std::time::Duration::from_secs(config.auth_context_cache_ttl_seconds));
    // Same optional-capability boundary as the routers merged below — `GET
    // /metadata/openapi.json` (`metap_http::routes::metadata::openapi_json`) only knows how to
    // describe its own static routes and the per-entity dynamic ones; it has zero knowledge of
    // `metap-lowcode-http`/`metap-control-http`'s paths, so this app (which does mount both)
    // hands their hand-written OpenAPI fragments in here explicitly. A downstream project that
    // skips one or both routers below should skip the matching `openapi_paths()` call too.
    state.extra_openapi_paths = Arc::new(
        metap::lowcode_http::openapi_paths::openapi_paths()
            .into_iter()
            .chain(metap::control_http::openapi_paths::openapi_paths())
            .collect(),
    );

    // gRPC — genuinely can't share the REST port (needs HTTP/2-only serving; see
    // `metap_grpc::serve`'s doc comment for why this crate deliberately runs its own listener
    // instead), so it's opt-in via env var + its own port, mirroring
    // `apps/jira-server/src/main.rs`'s identical block. This is what gives
    // `crates/graphql-gateway` a second real upstream to aggregate alongside jira-server — before
    // this, `crm-server` had no gRPC surface at all. Auth uses this app's own static per-app
    // RS256 keypair (`TokenVerifier::Static`) — the JWKS multi-service trust root (`metap-jwks`)
    // is for a deployment with several separately signing services, not needed for this demo
    // app's own gRPC surface. Read from `state` before it's moved into `build_router` below.
    let grpc_handle = if env_flag_enabled("GRPC_ENABLED") {
        let grpc_port: u16 = std::env::var("GRPC_PORT")
            .ok()
            .and_then(|v| v.parse().ok())
            .unwrap_or(3001);
        let grpc_addr: std::net::SocketAddr = format!("{}:{grpc_port}", config.host).parse()?;
        let auth = metap::grpc::AuthConfig {
            verifier: metap::grpc::TokenVerifier::Static {
                decoding_key: (*state.jwt_decoding_key).clone(),
                leeway: 20,
            },
            router: state.router.clone(),
            auth_context_entity: state.auth_context_entity.as_deref().map(str::to_string),
            context_attributes_cache: state.context_attributes_cache.clone(),
        };
        let service = metap::grpc::GrpcRecordService::new(state.crud.clone(), auth);
        tracing::info!(%grpc_addr, "gRPC listening");
        Some(tokio::spawn(async move {
            if let Err(err) = metap::grpc::serve(grpc_addr, service, None).await {
                tracing::error!(error = %err, "gRPC server exited with error");
            }
        }))
    } else {
        None
    };

    // `metap::lowcode_http::router()` is the low-code control plane's admin API
    // (`docs/roadmap.md` Phase 11 / Phase A) and `metap::control_http::router()` is the
    // platform-tenant provisioning API (Phase 16 Giai đoạn 3) — both optional platform
    // capabilities, not core; `build_router` itself has zero knowledge of either (see that
    // function's doc comment). This demo app opts into both; a downstream project that wants
    // neither can pass `Router::new()` here instead and never link the corresponding `-http`
    // crates in.
    let mut router = build_router(
        state,
        &config.cors_origins,
        metap::lowcode_http::router().merge(metap::control_http::router()),
    );

    if let Some(dir) = &config.static_dir {
        if std::path::Path::new(dir).is_dir() {
            tracing::info!(dir, "serving frontend static files");
            let index_html = format!("{dir}/index.html");
            // `.fallback()`, not `.not_found_service()` — the latter always forces the
            // response status to 404 (see its doc comment), which is wrong for SPA
            // client-side routes: the browser needs a real 200 to render `index.html`
            // normally instead of treating it as an error page.
            router = router.fallback_service(ServeDir::new(dir).fallback(ServeFile::new(index_html)));
        } else {
            tracing::warn!(
                dir,
                "STATIC_DIR is set but is not a directory, skipping static file serving"
            );
        }
    }

    // Off by default — the standalone `notification-worker` binary (`pnpm
    // worker:notification:rs`) is the normal deployment shape, matching `outbox-publisher`.
    // Set NOTIFICATION_WORKER_INLINE=true to run it as a background task in this same
    // process instead, for single-process/monolithic deployments (same pattern as
    // `pnpm start`'s STATIC_DIR merging crm-fe into this binary) — both modes call the exact
    // same `notification_worker::run`, so they can't drift apart.
    let notification_worker_handle = if env_flag_enabled("NOTIFICATION_WORKER_INLINE") {
        tracing::info!("starting notification worker (inline mode)...");
        let url = config.rabbitmq_url.clone();
        Some(tokio::spawn(async move {
            let connect = move || {
                let url = url.clone();
                async move { RabbitEventBus::connect(&url).await }
            };
            if let Err(err) = notification_worker::run(connect, shutdown_signal()).await {
                tracing::error!(error = %err, "notification worker exited with error");
            }
        }))
    } else {
        None
    };

    let addr = format!("{}:{}", config.host, config.port);
    let listener = tokio::net::TcpListener::bind(&addr).await?;
    tracing::info!(%addr, "listening");

    // `build_router`'s rate-limit layer keys on peer IP via `ConnectInfo<SocketAddr>` — see
    // `metap_http::build_router`'s doc comment. Plain `into_make_service()` wouldn't
    // populate that extension and every request would fail rate-limit key extraction.
    axum::serve(
        listener,
        router.into_make_service_with_connect_info::<std::net::SocketAddr>(),
    )
    .with_graceful_shutdown(shutdown_signal())
    .await?;

    // Block on the inline notification worker's own shutdown branch (and its
    // `bus.close()`) instead of letting the process exit as soon as the HTTP server drains —
    // otherwise the spawned task above can be cut off mid-message.
    if let Some(handle) = notification_worker_handle {
        handle.await.ok();
    }

    // `grpc_handle` deliberately isn't joined here — see `apps/jira-server/src/main.rs`'s
    // identical `drop(grpc_handle)` for why: no in-flight-publish state to drain, and
    // `metap_grpc::serve` has no shutdown-signal parameter to wire one in with.
    drop(grpc_handle);

    Ok(())
}

async fn shutdown_signal() {
    tokio::signal::ctrl_c().await.ok();
    tracing::info!("shutdown signal received, exiting");
}

fn env_flag_enabled(name: &str) -> bool {
    std::env::var(name).is_ok_and(|v| v == "true" || v == "1")
}
