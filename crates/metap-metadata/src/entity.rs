//! JSON field names (camelCase) and `FieldKind`'s wire values are what a served
//! `/metadata/openapi.json` and `/metadata/entities` response need to produce for
//! `packages/platform-react`'s `openapi-typescript` codegen to keep working.
//!
//! Deliberately has no `schema` field — request-payload validation is a validator
//! generated directly from `fields` (kind/required/enumValues, see
//! `crates/metap-crud/src/validation.rs`) rather than a hand-authored, separately-maintained
//! schema. That validator is `CrudService`-layer work, not part of the metadata shape
//! itself.

use serde::{Deserialize, Serialize};

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum FieldKind {
    Id,
    String,
    Number,
    Boolean,
    Date,
    Datetime,
    Money,
    Enum,
    Reference,
    Json,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct EntityField {
    pub name: String,
    pub label: String,
    pub kind: FieldKind,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub required: Option<bool>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub indexed: Option<bool>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub unique: Option<bool>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub enum_values: Option<Vec<String>>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub ref_entity: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub ref_display_field: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub searchable: Option<bool>,
    /// "substring" (default) or "fts" — only meaningful when `searchable: true`.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub search_mode: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub sortable: Option<bool>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct EntityListView {
    pub name: String,
    pub label: String,
    pub fields: Vec<String>,
    pub filters: Vec<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub default_sort: Option<String>,
    pub max_limit: u32,
}

/// `guard` is a `PolicyCondition` (`metap-permission`, the same declarative type policies
/// use) rather than a server-side predicate function — this is why this crate depends on
/// `metap-permission`. It crosses the wire like any other field (`GET /metadata/entities` is
/// authenticated, not admin-gated, so any tenant user can already see it — same exposure
/// level as the `capabilities.transitions` guard *results* `CrudService::get` already
/// computes from it). Unlike the old TS-era shape (`entity-wire-schema.ts`, where `guard` was
/// a server-side predicate *function* and genuinely couldn't be serialized), the Rust port's
/// guard was always data — it stayed `#[serde(skip)]` here only to mirror that old exclusion,
/// not because anything about `PolicyCondition` prevents it. Un-skipped 2026-08-17 (Phase 11
/// Phase B, `docs/roadmap.md`) so DB-authored (low-code) entities can carry a workflow with
/// guards at all — `metap-lowcode`'s `LowCodeEntityDefinition` round-trips this same struct
/// through Postgres `jsonb`, and `#[serde(skip)]` would have silently dropped every guard on
/// every save. `metap_workflow::run_guard` was already entity-agnostic before this change
/// (it evaluates a `PolicyCondition` against record data + context, no code-authored
/// assumption anywhere) — this was purely a serialization gap, not a runtime one.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct WorkflowTransition {
    pub action: String,
    pub from: String,
    pub to: String,
    pub label: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub guard: Option<metap_permission::PolicyCondition>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct EntityWorkflow {
    pub state_field: String,
    pub initial_state: String,
    pub terminal_states: Vec<String>,
    pub transitions: Vec<WorkflowTransition>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct EntityDefinition {
    pub name: String,
    pub label: String,
    pub table_name: String,
    pub fields: Vec<EntityField>,
    pub list_views: Vec<EntityListView>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub workflow: Option<EntityWorkflow>,
}
