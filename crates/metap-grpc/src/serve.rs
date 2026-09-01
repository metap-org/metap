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

use metap_control::{ContextAttributesCache, Router};
use metap_crud::CrudService;
use tonic::transport::{Server, ServerTlsConfig};

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
    builder.add_service(RecordServiceServer::new(service)).serve(addr).await
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
