//! The shape an operator authors through the metadata admin API (`docs/roadmap.md` Phase 11
//! / Phase A). Deliberately reuses `metap_metadata::{EntityField, EntityListView}` rather than
//! a parallel field-shape type — those already derive `Deserialize`, so JSON shape validation
//! comes for free from serde instead of a hand-maintained schema (the TS-era spec used a
//! separate Zod schema for exactly this reason; Rust doesn't need the duplication). No
//! `tableName` (always `"records"`, the generic table every entity lives in) and no
//! `workflow` (DB-authored entities don't support workflow in Phase A — `WorkflowTransition`'s
//! `guard` is a `PolicyCondition` evaluated by code paths that assume a code-authored entity
//! today; declarative workflow guards for DB-authored entities are Phase B work).

use metap_metadata::{validate, EntityDefinition, EntityField, EntityListView, MetadataValidationError};
use serde::{Deserialize, Serialize};

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct LowCodeEntityDefinition {
    pub name: String,
    pub label: String,
    pub fields: Vec<EntityField>,
    #[serde(default)]
    pub list_views: Vec<EntityListView>,
}

impl LowCodeEntityDefinition {
    pub fn to_entity_definition(&self) -> EntityDefinition {
        EntityDefinition {
            name: self.name.clone(),
            label: self.label.clone(),
            table_name: "records".to_string(),
            fields: self.fields.clone(),
            list_views: self.list_views.clone(),
            workflow: None,
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
