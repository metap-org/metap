//! Generic, metadata-driven CRUD-over-gRPC — the service-to-service transport counterpart to
//! REST (`crates/metap-http`). One `RecordService` (see `proto/metap_crud.proto`'s doc comment
//! for why not `CrudService`) exposing `list`/`get`/`create`/`update`/`transition`/`delete` for
//! *any* entity a `MetadataRegistry` knows about — no per-entity proto/codegen, mirroring how
//! `/api/:entity*` and `/metadata/openapi.json` are already entity-generic. Every RPC calls
//! straight into `metap_crud::CrudService`, the same protocol-agnostic core REST already uses,
//! so permission enforcement/validation/optimistic-locking/workflow behavior can't drift between
//! the two transports.
//!
//! Entirely opt-in, like `metap-jwks`/`metap-jwks-http`: `crates/metap-http`, `../metap-demo-crm`,
//! and `../metap-demo-jira` have zero dependency on this crate. A binary that wants gRPC runs
//! [`serve`] in its own `tokio::spawn`'d task on a second port, alongside its main HTTP listener
//! — see `serve`'s doc comment for why a second port rather than unifying onto axum's own
//! server.

pub mod pb {
    tonic::include_proto!("metap.crud.v1");
}

mod auth;
pub mod client;
pub mod convert;
mod list_input;
mod serve;
mod service;
mod status;

pub use auth::{AuthConfig, TokenVerifier};
pub use client::{GrpcBackend, ServiceTokenSource};
pub use serve::{optional_serve, serve, OptionalServeConfig};
pub use service::GrpcRecordService;
