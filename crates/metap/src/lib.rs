//! Facade over metap's `crates/metap-*` sub-crates (see CLAUDE.md's Monorepo Layout for
//! what each one owns) — one dependency, one import, instead of a downstream binary having
//! to know which of ~8 sub-crates each type or function lives in. This crate has no logic
//! of its own on purpose: every item here is a re-export, the sub-crates stay the real
//! implementation and stay independently usable if a caller wants only one piece — nothing
//! this crate does is unique to it. `prelude` covers what a boot sequence shaped like
//! `apps/crm-server/src/main.rs` needs; anything else is reachable through the namespaced
//! modules below (`metap::query::plan_list`, `metap::workflow::run_guard`, etc.) without
//! adding that sub-crate as its own direct dependency.

pub use metap_control as control;
pub use metap_control_http as control_http;
pub use metap_crud as crud;
pub use metap_http as http;
pub use metap_infra as infra;
pub use metap_lowcode as lowcode;
pub use metap_lowcode_http as lowcode_http;
pub use metap_metadata as metadata;
pub use metap_peripherals as peripherals;
pub use metap_permission as permission;
pub use metap_query as query;
pub use metap_workflow as workflow;

pub mod prelude {
    //! `use metap::prelude::*;` covers registering entities, building `AppState`, and
    //! wiring `metap_http::build_router` — the full shape of a boot sequence like
    //! `apps/crm-server/src/main.rs`. A few names exist on more than one sub-crate (e.g.
    //! `JsonObject` on `metap-crud`/`metap-permission`/`metap-workflow` — all aliases of
    //! `serde_json::Map<String, Value>`, not actually distinct types, but Rust still treats
    //! them as separate items and refuses to glob-import both under one name) — only one
    //! wins the unqualified name here; reach the others through their namespaced module
    //! (`metap::permission::JsonObject`, `metap::workflow::JsonObject`).
    pub use metap_crud::{CrudService, JsonObject, RecordCapabilities, RecordDto};
    pub use metap_http::{build_router, AdminContext, AppState, AuthContext, PlatformAdminContext};
    pub use metap_infra::{connect_db, load_config, AppConfig};
    pub use metap_metadata::{
        EntityDefinition, EntityField, EntityListView, EntityWorkflow, FieldKind, MetadataRegistry, WorkflowTransition,
    };
    pub use metap_peripherals::{check_metadata_drift, reconcile_indexes};
    pub use metap_permission::{PermissionService, PolicyCondition, PostgresPolicyStore};
}
