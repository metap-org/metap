//! Runs `GrpcRecordService` on its own listener — a second port alongside the binary's main
//! REST (and optional GraphQL) listener, matching the "second `tokio::spawn`'d listener in the
//! same process" shape the plan behind this crate settled on (rather than trying to unify gRPC
//! onto axum's own hyper server, which would force the whole app onto HTTP/2-only serving for no
//! real benefit in a containerized deployment where a second `containerPort` costs nothing). A
//! binary opts in with something like:
//!
//! ```ignore
//! if config.grpc_enabled {
//!     let service = metap::grpc::GrpcRecordService::new(crud.clone(), auth_config);
//!     tokio::spawn(metap::grpc::serve(grpc_addr, service, tls_config));
//! }
//! ```

use std::net::SocketAddr;

use tonic::transport::{Server, ServerTlsConfig};

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
