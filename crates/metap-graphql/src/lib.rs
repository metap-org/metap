//! Generic, metadata-driven GraphQL — the BFF-facing transport counterpart to REST
//! (`crates/metap-http`) and gRPC (`crates/metap-grpc`). One schema, built dynamically from a
//! `MetadataRegistry` at boot (and rebuilt whenever it hot-swaps — see `metap-graphql-http` for
//! that wiring), exposing `get`/`list`/`create`/`update`/`delete`/`transition` for *every*
//! registered entity — no per-entity codegen, mirroring how `/api/:entity*` and
//! `/metadata/openapi.json` are already entity-generic. Every resolver calls straight into
//! `metap_crud::CrudService`, the same protocol-agnostic core REST and gRPC already use, so
//! permission enforcement/validation/optimistic-locking/workflow behavior can't drift between
//! transports.
//!
//! Entirely opt-in, like `metap-jwks`/`metap-grpc`: `crates/metap-http`, `../metap-demo-crm`, and
//! `../metap-demo-jira` have zero dependency on this crate. See `schema.rs`'s doc comment for the
//! three mandatory pieces a GraphQL layer on this platform needs (DataLoader batching, query
//! complexity/depth limits, field-level permission masking) and how each is satisfied.

mod composite_backend;
mod list_input;
mod loader;
mod naming;
mod record_handle;
mod schema;
mod type_map;

pub use composite_backend::CompositeBackend;
pub use loader::{RecordKey, RecordLoader};
pub use schema::{build_schema, with_request_data, SchemaLimits};
pub use type_map::JSON_SCALAR;

// Re-exported so a downstream binary only needs this crate, not a direct `async-graphql`
// dependency, to wire the schema into its own HTTP handler (`metap-graphql-http` already does
// this; a binary that wants to bypass that crate's axum glue can use these directly). `Schema`
// is specifically the `dynamic` module's — the one `build_schema` returns — not
// `async_graphql::Schema<Query, Mutation, Subscription>` (the derive-macro/static one, unused
// anywhere on this platform since entities aren't known at compile time).
pub use async_graphql::dynamic::Schema;
pub use async_graphql::dynamic::SchemaError;
pub use async_graphql::{Request, Response};
