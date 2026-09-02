//! Facade over metap's `crates/metap-*` sub-crates (see CLAUDE.md's Monorepo Layout for
//! what each one owns) — one dependency, one import, instead of a downstream binary having
//! to know which sub-crate each type or function lives in. This crate has no logic
//! of its own on purpose: every item here is a re-export, the sub-crates stay the real
//! implementation and stay independently usable if a caller wants only one piece — nothing
//! this crate does is unique to it. `prelude` covers what a boot sequence shaped like
//! `../metap-demo-crm/src/main.rs` needs; anything else is reachable through the namespaced
//! modules below (`metap::query::plan_list`, `metap::workflow::run_guard`, etc.) without
//! adding that sub-crate as its own direct dependency.
//!
//! **Does NOT re-export `metap-lowcode`/`metap-lowcode-http`/`metap-control-http`
//! (2026-08-31, `docs/features/07-split-lowcode-saas-crates.md`)** — those 3 crates plus
//! `reconciler-orchestrator` moved to `../metap-lowcode`, a separate repo depending
//! on this one via `path` (same pattern `../metap-demo-crm`/`../metap-demo-jira` already use).
//! No core crate this facade re-exports depends on any of the 4 — `metap-http`'s
//! `AppState.metadata: Arc<ArcSwap<MetadataRegistry>>` is the generic seam
//! `metap-lowcode-http::apply_registry` writes into, with zero knowledge of `metap-lowcode`
//! on this side. A consumer that wants the low-code layer (currently only
//! `../metap-demo-crm`) depends on those 3 crates directly, not through this facade.

pub use metap_app as app;
pub use metap_attachments as attachments;
pub use metap_auth as tenant_auth;
pub use metap_cache as cache;
pub use metap_control as control;
pub use metap_cron as cron;
pub use metap_crud as crud;
pub use metap_dashboards as dashboards;
pub use metap_graphql as graphql;
pub use metap_graphql_http as graphql_http;
pub use metap_grpc as grpc;
pub use metap_http as http;
pub use metap_infra as infra;
pub use metap_jwks as jwks;
pub use metap_jwks_http as jwks_http;
pub use metap_metadata as metadata;
pub use metap_peripherals as peripherals;
pub use metap_permission as permission;
pub use metap_query as query;
pub use metap_reconciler as reconciler;
pub use metap_runtime as runtime;
pub use metap_storage as storage;
pub use metap_workflow as workflow;

pub mod prelude {
    //! `use metap::prelude::*;` covers registering entities, building `AppState`, and
    //! wiring `metap_http::build_router` — the full shape of a boot sequence like
    //! `../metap-demo-crm/src/main.rs`. A few names exist on more than one sub-crate (e.g.
    //! `JsonObject` on `metap-crud`/`metap-permission`/`metap-workflow` — all aliases of
    //! `serde_json::Map<String, Value>`, not actually distinct types, but Rust still treats
    //! them as separate items and refuses to glob-import both under one name) — only one
    //! wins the unqualified name here; reach the others through their namespaced module
    //! (`metap::permission::JsonObject`, `metap::workflow::JsonObject`).
    pub use metap_app::{bootstrap_platform, PlatformParts};
    pub use metap_control::PostgresPolicyStore;
    pub use metap_crud::{CrudService, JsonObject, RecordCapabilities, RecordDto};
    pub use metap_http::{build_router, AdminContext, AppState, AuthContext, PlatformAdminContext};
    pub use metap_infra::{connect_db, load_config, AppConfig};
    pub use metap_metadata::{
        submit_entity, submit_field_display_hints, submit_related_views, EntityDefinition, EntityField,
        EntityListView, EntityWorkflow, FieldDisplayHint, FieldKind, MetadataRegistry, RelatedView,
        WorkflowTransition,
    };
    pub use metap_peripherals::{check_metadata_drift, reconcile_indexes};
    pub use metap_permission::{PermissionService, PolicyCondition};
}
