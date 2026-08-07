//! Mirrors `packages/core/src/core/metadata/entity.ts` field-for-field. JSON field names
//! (camelCase) and `FieldKind`'s wire values match `entity-wire-schema.ts` exactly, since
//! this is what a Rust-served `/metadata/openapi.json` and `/metadata/entities` response
//! need to produce for `packages/platform-react`'s `openapi-typescript` codegen to keep
//! working unchanged (see `docs/rust-core-viability.md`'s Schema & Codegen Strategy).
//!
//! Deliberately has no `schema` field the way `EntityDefinition<TInput>` does in TS —
//! that Zod schema's only job was request-payload validation, which
//! `docs/rust-core-viability.md` scopes as a validator generated directly from `fields`
//! (kind/required/enumValues) rather than a hand-authored, separately-maintained schema.
//! That validator is CrudService-layer work (Migration Order step 7), not part of the
//! metadata shape itself.

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

/// `guard` is `#[serde(skip)]` — it never crosses the wire (matches
/// `entity-wire-schema.ts`'s exclusion, and `MetadataCompiler::hash`'s exclusion, since
/// `#[serde(skip)]` also drops it from the JSON `compiler::hash` serializes). Where TS
/// modeled a guard as a server-side predicate *function*, this is a `PolicyCondition` — the
/// declarative shape `docs/rust-core-viability.md`'s Workflow finding recommended reusing
/// (from `metap-permission`, the same type policies already use) rather than porting a
/// function-guard model Rust has no equivalent representation for. This is why this crate
/// depends on `metap-permission` — mirroring `entity.ts`'s existing (TS) dependency on
/// `permission-service.ts` for the same reason, not a new layering decision.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct WorkflowTransition {
    pub action: String,
    pub from: String,
    pub to: String,
    pub label: String,
    #[serde(skip)]
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
