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
//!
//! **Records + aggregate only — not REST's full surface (audit 04 finding B3, confirmed
//! intentional 2026-09-13, not a gap to close).** `crates/metap-http` has 15 route groups
//! (attachments, cron, dashboards, preferences, platform/tenant config, users, admin, auth, ...);
//! this crate and `metap-graphql` expose only the generic record CRUD + aggregate operations any
//! entity has. Both are BFF/service-to-service transports by design (see the module docs of
//! `crates/metap-graphql-gateway`, this crate's own real consumer) — a downstream service reading
//! or writing another service's *records* is a normal cross-service call; a downstream service
//! managing another service's *cron jobs* or *attachments* is a much larger surface with no real
//! caller today, and would be built when one actually needs it, not spun up speculatively to
//! match REST's shape 1:1.

pub mod pb {
    tonic::include_proto!("metap.crud.v1");
}

mod auth;
pub mod client;
pub mod convert;
mod list_input;
mod rate_limit;
mod serve;
mod service;
mod status;

pub use auth::{AuthConfig, TokenVerifier};
pub use client::{GrpcBackend, ServiceTokenSource};
pub use serve::{optional_serve, serve, OptionalServeConfig};
pub use service::GrpcRecordService;
