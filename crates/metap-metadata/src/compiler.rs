//! Mirrors `packages/core/src/core/metadata/metadata-compiler.ts`: boot-time structural
//! validation and a deterministic content hash, both driven purely by entity metadata
//! (never request input) — same contract, same issue messages where the shape allows it.

use std::collections::HashSet;

use sha2::{Digest, Sha256};

use crate::entity::{EntityDefinition, EntityField, EntityListView, EntityWorkflow};

/// Always present on every entity's underlying `records` row regardless of declared
/// fields — see `QueryPlanner`'s `sortableFields`/`fieldExpression` in the TS codebase,
/// which resolve these to real DB columns independent of `entity.fields`.
const IMPLICIT_SYSTEM_FIELDS: [&str; 2] = ["createdAt", "updatedAt"];

#[derive(Debug, Clone)]
pub struct MetadataValidationError {
    pub entity: String,
    pub issues: Vec<String>,
}

impl std::fmt::Display for MetadataValidationError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(
            f,
            "Invalid metadata for entity \"{}\": {}",
            self.entity,
            self.issues.join("; ")
        )
    }
}

impl std::error::Error for MetadataValidationError {}

#[derive(serde::Serialize)]
struct HashShape<'a> {
    label: &'a str,
    fields: &'a [EntityField],
    #[serde(rename = "listViews")]
    list_views: &'a [EntityListView],
    #[serde(skip_serializing_if = "Option::is_none")]
    workflow: Option<&'a EntityWorkflow>,
}

/// Deterministic SHA-256 over the entity's shape — `label`/`fields`/`listViews`/`workflow`
/// (guard-free by construction, see `entity.rs`). `serde_json::Value`'s object map is a
/// `BTreeMap` by default (no `preserve_order` feature enabled anywhere in this workspace),
/// so `to_string` on a `Value` built via `to_value` emits keys in sorted order at every
/// level — the same guarantee `stableStringify`'s explicit `Object.keys(value).sort()`
/// gives the TS implementation. Array element order is preserved unsorted in both.
pub fn hash(entity: &EntityDefinition) -> anyhow::Result<String> {
    let shape = HashShape {
        label: &entity.label,
        fields: &entity.fields,
        list_views: &entity.list_views,
        workflow: entity.workflow.as_ref(),
    };
    let value = serde_json::to_value(&shape)?;
    let stable_json = serde_json::to_string(&value)?;
    let digest = Sha256::digest(stable_json.as_bytes());
    Ok(hex::encode(digest))
}

pub fn validate(entity: &EntityDefinition) -> Result<(), MetadataValidationError> {
    let mut issues = Vec::new();
    let mut field_names: HashSet<&str> = HashSet::new();

    for field in &entity.fields {
        if !field_names.insert(field.name.as_str()) {
            issues.push(format!("duplicate field name \"{}\"", field.name));
        }

        if matches!(field.kind, crate::entity::FieldKind::Enum)
            && field.enum_values.as_ref().map(|v| v.is_empty()).unwrap_or(true)
        {
            issues.push(format!(
                "field \"{}\" is kind \"enum\" but declares no enumValues",
                field.name
            ));
        }

        if matches!(field.kind, crate::entity::FieldKind::Reference) && field.ref_entity.is_none()
        {
            issues.push(format!(
                "field \"{}\" is kind \"reference\" but declares no refEntity",
                field.name
            ));
        }
    }

    let is_known_field = |name: &str| -> bool {
        field_names.contains(name) || IMPLICIT_SYSTEM_FIELDS.contains(&name)
    };

    for list_view in &entity.list_views {
        for field_name in &list_view.fields {
            if !is_known_field(field_name) {
                issues.push(format!(
                    "listView \"{}\" references unknown field \"{}\"",
                    list_view.name, field_name
                ));
            }
        }
        for field_name in &list_view.filters {
            if !is_known_field(field_name) {
                issues.push(format!(
                    "listView \"{}\" filters on unknown field \"{}\"",
                    list_view.name, field_name
                ));
            }
        }
        if let Some(default_sort) = &list_view.default_sort {
            let sort_field = default_sort.strip_prefix('-').unwrap_or(default_sort);
            if !is_known_field(sort_field) {
                issues.push(format!(
                    "listView \"{}\" defaultSort references unknown field \"{}\"",
                    list_view.name, sort_field
                ));
            }
        }
    }

    if let Some(workflow) = &entity.workflow {
        if !field_names.contains(workflow.state_field.as_str()) {
            issues.push(format!(
                "workflow.stateField \"{}\" is not a declared field",
                workflow.state_field
            ));
        }
        if workflow.initial_state.is_empty() {
            issues.push("workflow.initialState must be a non-empty string".to_string());
        }
        for state in &workflow.terminal_states {
            if state.is_empty() {
                issues.push("workflow.terminalStates contains an empty string".to_string());
            }
        }

        let mut seen_transition_keys: HashSet<String> = HashSet::new();
        for transition in &workflow.transitions {
            if transition.from.is_empty() || transition.to.is_empty() || transition.action.is_empty()
            {
                issues.push(format!(
                    "workflow transition is missing from/to/action: {{\"from\":\"{}\",\"to\":\"{}\",\"action\":\"{}\"}}",
                    transition.from, transition.to, transition.action
                ));
                continue;
            }
            let key = format!("{}::{}", transition.from, transition.action);
            if !seen_transition_keys.insert(key) {
                issues.push(format!(
                    "duplicate transition action \"{}\" from state \"{}\"",
                    transition.action, transition.from
                ));
            }
        }
    }

    if issues.is_empty() {
        Ok(())
    } else {
        Err(MetadataValidationError { entity: entity.name.clone(), issues })
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::entity::{EntityField, FieldKind};

    fn field(name: &str, kind: FieldKind) -> EntityField {
        EntityField {
            name: name.to_string(),
            label: name.to_string(),
            kind,
            required: None,
            indexed: None,
            unique: None,
            enum_values: None,
            ref_entity: None,
            ref_display_field: None,
            searchable: None,
            search_mode: None,
            sortable: None,
        }
    }

    fn minimal_entity() -> EntityDefinition {
        EntityDefinition {
            name: "test.thing".to_string(),
            label: "Thing".to_string(),
            table_name: "records".to_string(),
            fields: vec![field("name", FieldKind::String)],
            list_views: vec![],
            workflow: None,
        }
    }

    #[test]
    fn valid_entity_passes() {
        assert!(validate(&minimal_entity()).is_ok());
    }

    #[test]
    fn duplicate_field_name_is_rejected() {
        let mut entity = minimal_entity();
        entity.fields.push(field("name", FieldKind::String));
        let err = validate(&entity).unwrap_err();
        assert!(err.issues.iter().any(|i| i.contains("duplicate field name")));
    }

    #[test]
    fn enum_without_values_is_rejected() {
        let mut entity = minimal_entity();
        entity.fields.push(field("status", FieldKind::Enum));
        let err = validate(&entity).unwrap_err();
        assert!(err.issues.iter().any(|i| i.contains("no enumValues")));
    }

    #[test]
    fn list_view_unknown_field_is_rejected() {
        let mut entity = minimal_entity();
        entity.list_views.push(EntityListView {
            name: "default".to_string(),
            label: "Default".to_string(),
            fields: vec!["nope".to_string()],
            filters: vec![],
            default_sort: None,
            max_limit: 50,
        });
        let err = validate(&entity).unwrap_err();
        assert!(err.issues.iter().any(|i| i.contains("unknown field \"nope\"")));
    }

    #[test]
    fn list_view_implicit_system_field_is_allowed() {
        let mut entity = minimal_entity();
        entity.list_views.push(EntityListView {
            name: "default".to_string(),
            label: "Default".to_string(),
            fields: vec!["createdAt".to_string()],
            filters: vec![],
            default_sort: None,
            max_limit: 50,
        });
        assert!(validate(&entity).is_ok());
    }

    #[test]
    fn hash_is_deterministic_and_order_independent_across_calls() {
        let entity = minimal_entity();
        assert_eq!(hash(&entity).unwrap(), hash(&entity).unwrap());
    }

    #[test]
    fn hash_changes_when_shape_changes() {
        let entity = minimal_entity();
        let mut changed = minimal_entity();
        changed.fields.push(field("extra", FieldKind::String));
        assert_ne!(hash(&entity).unwrap(), hash(&changed).unwrap());
    }
}
