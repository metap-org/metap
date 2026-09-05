//! Runs `GrpcRecordService` on its own listener — a second port alongside the binary's main
//! REST (and optional GraphQL) listener, matching the "second `tokio::spawn`'d listener in the
//! same process" shape the plan behind this crate settled on (rather than trying to unify gRPC
//! onto axum's own hyper server, which would force the whole app onto HTTP/2-only serving for no
//! real benefit in a containerized deployment where a second `containerPort` costs nothing).
//!
//! Most binaries want [`optional_serve`] — the `GRPC_ENABLED`/`GRPC_PORT`-gated,
//! static-per-app-keypair-auth convenience wrapper, e.g.:
//!
//! ```ignore
//! let grpc_handle = metap::grpc::optional_serve(
//!     &config.host,
//!     3001,
//!     metap::grpc::OptionalServeConfig {
//!         crud: state.crud.clone(),
//!         router: state.router.clone(),
//!         jwt_decoding_key: state.jwt_decoding_key.clone(),
//!         auth_context_entity: state.auth_context_entity.as_deref().map(str::to_string),
//!         context_attributes_cache: state.context_attributes_cache.clone(),
//!     },
//! )
//! .await?;
//! ```
//!
//! A binary that needs the JWKS multi-service trust root (`TokenVerifier::Jwks`) instead, or
//! wants gRPC unconditionally on rather than env-gated, builds `AuthConfig`/calls [`serve`]
//! directly:
//!
//! ```ignore
//! if config.grpc_enabled {
//!     let service = metap::grpc::GrpcRecordService::new(crud.clone(), auth_config);
//!     tokio::spawn(metap::grpc::serve(grpc_addr, service, tls_config));
//! }
//! ```

use std::net::SocketAddr;
use std::sync::Arc;
use std::time::Duration;

use metap_control::{ContextAttributesCache, Router};
use metap_crud::CrudService;
use tonic::transport::{Server, ServerTlsConfig};
use tower_http::classify::GrpcFailureClass;
use tower_http::trace::TraceLayer;
use tracing::Span;

use crate::auth::{AuthConfig, TokenVerifier};
use crate::pb::record_service_server::RecordServiceServer;
use crate::service::GrpcRecordService;

/// `tls_config` is `None` for a deployment that hasn't set up mTLS yet (or is relying on
/// network-level trust — a private VPC/service mesh — instead); `Some` wires client-cert
/// verification for the machine-to-machine case this crate's auth design calls for (see
/// `auth.rs`'s doc comment for why deriving identity from the cert itself is still a deferred
/// extension point, not implemented here).
pub async fn serve(
    addr: SocketAddr,
    service: GrpcRecordService,
    tls_config: Option<ServerTlsConfig>,
) -> Result<(), tonic::transport::Error> {
    let mut builder = Server::builder();
    if let Some(tls) = tls_config {
        builder = builder.tls_config(tls)?;
    }
    // Per-RPC access log — until this, a call arriving here left literally no trace in this
    // process's own log, only the one-time "gRPC listening" line above ever printed. Found live
    // 2026-09-06: a cross-service GraphQL field (routed to a remote upstream via
    // `metap_grpc::client::GrpcBackend`, not REST) produced zero log output on the *target*
    // service's side, unlike `metap-http::build_router`'s `metap_runtime::trace` layer, which
    // every REST route already gets. `new_for_grpc()`'s classifier reads the `grpc-status`
    // trailer once the response stream actually completes, not the outer HTTP/2 status (always
    // `200` for a well-formed gRPC response either way) — so `on_failure` below still fires
    // correctly for a real RPC error even though `on_response` only ever sees "200".
    //
    // `entity`/`record_id` start `Empty` because this layer only ever sees the raw HTTP/2
    // envelope, before tonic has even decoded the protobuf body — every entity's List/Get/Create/
    // ... shares the exact same RPC name (`method` below is always e.g.
    // `/metap.crud.v1.RecordService/List`, generic-over-entity by design, same reason
    // `metap-http`'s REST routes are all `/api/:entity*`), so without these 2 fields the access
    // log alone can't tell a `waf.zones` call from a `waf.scan_jobs` one — found live 2026-09-06,
    // reported as "không biết nó gọi vào đâu" against the very first version of this layer.
    // `service.rs`'s `record_span_fields` fills them in once the handler has actually decoded the
    // request, from *inside* this same span (tonic/tower_http run the whole request future
    // `.instrument()`-ed by it) — `Span::record` on a field this macro didn't declare is a silent
    // no-op, which is why both names must be reserved here even though this layer itself never
    // sets them.
    let trace = TraceLayer::new_for_grpc()
        .make_span_with(|request: &http::Request<tonic::body::Body>| {
            tracing::info_span!(
                "grpc_request",
                method = %request.uri().path(),
                entity = tracing::field::Empty,
                record_id = tracing::field::Empty,
                latency_ms = tracing::field::Empty,
            )
        })
        .on_response(
            |_response: &http::Response<tonic::body::Body>, latency: Duration, span: &Span| {
                span.record("latency_ms", latency.as_millis() as u64);
                tracing::event!(parent: span, tracing::Level::INFO, "request completed");
            },
        )
        .on_failure(|error: GrpcFailureClass, latency: Duration, span: &Span| {
            span.record("latency_ms", latency.as_millis() as u64);
            tracing::event!(parent: span, tracing::Level::ERROR, %error, "request failed");
        });
    builder
        .layer(trace)
        .add_service(RecordServiceServer::new(service))
        .serve(addr)
        .await
}

/// Bundles what [`optional_serve`] needs from a binary's own composition root — grouped the same
/// way [`AuthConfig`] bundles its own inputs (see that struct's doc comment) rather than 5 loose
/// parameters.
pub struct OptionalServeConfig {
    pub crud: Arc<CrudService>,
    pub router: Router,
    pub jwt_decoding_key: Arc<jsonwebtoken::DecodingKey>,
    pub auth_context_entity: Option<String>,
    pub context_attributes_cache: ContextAttributesCache,
}

/// Convenience wrapper around [`serve`] for the common case: read `GRPC_ENABLED`/`GRPC_PORT`
/// (falling back to `default_port`) to decide whether to run gRPC at all, authenticate with the
/// binary's own static per-app keypair (`TokenVerifier::Static`), and spawn [`serve`] in its own
/// task. Found byte-near-identical (only `default_port`/`auth_context_entity` genuinely differed)
/// in both `../metap-demo-crm`'s and `../metap-demo-jira`'s `main.rs` before this existed.
/// Returns `Ok(None)` (no task spawned) when `GRPC_ENABLED` isn't set. A binary that needs
/// `TokenVerifier::Jwks` instead, or wants gRPC unconditionally on, still builds `AuthConfig`/
/// calls [`serve`] directly — this helper doesn't cover those cases.
pub async fn optional_serve(
    host: &str,
    default_port: u16,
    config: OptionalServeConfig,
) -> anyhow::Result<Option<tokio::task::JoinHandle<()>>> {
    if !metap_runtime::env::flag_enabled("GRPC_ENABLED") {
        return Ok(None);
    }
    let grpc_port: u16 = metap_runtime::env::env_or("GRPC_PORT", default_port);
    let grpc_addr: SocketAddr = format!("{host}:{grpc_port}").parse()?;
    let auth = AuthConfig {
        verifier: TokenVerifier::Static {
            decoding_key: (*config.jwt_decoding_key).clone(),
            leeway: 20,
        },
        router: config.router,
        auth_context_entity: config.auth_context_entity,
        context_attributes_cache: config.context_attributes_cache,
    };
    let service = GrpcRecordService::new(config.crud, auth);
    tracing::info!(%grpc_addr, "gRPC listening");
    Ok(Some(tokio::spawn(async move {
        if let Err(err) = serve(grpc_addr, service, None).await {
            tracing::error!(error = %err, "gRPC server exited with error");
        }
    })))
}
