//! The shape an operator authors through the metadata admin API (`docs/roadmap.md` Phase 11
//! / Phase A+B). Deliberately reuses `metap_metadata::{EntityField, EntityListView,
//! EntityWorkflow}` rather than parallel shape types — those already derive `Deserialize`, so
//! JSON shape validation comes for free from serde instead of a hand-maintained schema (the
//! TS-era spec used a separate Zod schema for exactly this reason; Rust doesn't need the
//! duplication). No `tableName` (always `"records"`, the generic table every entity lives
//! in). `workflow` (Phase B, 2026-08-17) reuses `EntityWorkflow` as-is, guard included —
//! `WorkflowTransition::guard`'s `#[serde(skip)]` was removed specifically so this round-trips
//! through the `jsonb` columns in `store.rs` (Phase A's caveat that DB-authored entities
//! "don't support workflow" no longer holds; `metap_workflow::run_guard` was always
//! entity-agnostic — see `entity.rs`'s doc comment on `guard` for the full story).

use metap_metadata::{
    validate, EntityDefinition, EntityField, EntityListView, EntityWorkflow, MetadataValidationError,
};
use serde::{Deserialize, Serialize};

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct LowCodeEntityDefinition {
    pub name: String,
    pub label: String,
    pub fields: Vec<EntityField>,
    #[serde(default)]
    pub list_views: Vec<EntityListView>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub workflow: Option<EntityWorkflow>,
}

impl LowCodeEntityDefinition {
    pub fn to_entity_definition(&self) -> EntityDefinition {
        EntityDefinition {
            name: self.name.clone(),
            label: self.label.clone(),
            table_name: "records".to_string(),
            fields: self.fields.clone(),
            list_views: self.list_views.clone(),
            workflow: self.workflow.clone(),
        }
    }

    /// Structural validation only (duplicate field names, enum fields with no `enumValues`,
    /// list views/workflow referencing unknown fields, ...) — reuses
    /// `metap_metadata::compiler::validate` verbatim rather than re-implementing it. Does
    /// *not* check cross-entity references (`refEntity` pointing at a real entity) — that
    /// needs a registry to check against, done by `publish`/`rollback` in `store.rs`.
    pub fn validate_shape(&self) -> Result<(), MetadataValidationError> {
        validate(&self.to_entity_definition())
    }
}
