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

/// Deterministic SHA-256 over the entity's shape — `label`/`fields`/`listViews`/`workflow`,
/// including each transition's `guard` since `entity.rs`'s `WorkflowTransition::guard` un-skip
/// (2026-08-17) — a guard-only edit now correctly bumps the hash / `version` at
/// `GET /metadata/entities`, where before it silently didn't. `serde_json::Value`'s object map is a
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

    // `field.name` is interpolated directly into SQL just like `table_name` below — as a real
    // column identifier (`metap-reconciler::compile`'s table-per-entity DDL, e.g.
    // `format!("\"{}\"", field.name)`) or as a JSONB path literal
    // (`data ->> '{field.name}'`, `metap-crud::find_referencing_record`) — but unlike
    // `table_name` had no charset check at all until this validation existed. Since a
    // low-code-authored entity's field names come from admin-supplied metadata (not a
    // developer's own source), an unvalidated name is a live SQL-injection vector at every one
    // of those interpolation sites. Every field name in this codebase is camelCase
    // (`customerId`, `dueDate`, ...), so this whitelist matches existing usage rather than
    // tightening it.
    fn is_safe_field_name(name: &str) -> bool {
        !name.is_empty()
            && name.chars().next().is_some_and(|c| c.is_ascii_alphabetic())
            && name.chars().all(|c| c.is_ascii_alphanumeric() || c == '_')
    }

    for field in &entity.fields {
        if !field_names.insert(field.name.as_str()) {
            issues.push(format!("duplicate field name \"{}\"", field.name));
        }

        if !is_safe_field_name(&field.name) {
            issues.push(format!(
                "field name \"{}\" must match ^[A-Za-z][A-Za-z0-9_]*$",
                field.name
            ));
        }

        if matches!(field.kind, crate::entity::FieldKind::Enum)
            && field.enum_values.as_ref().map(|v| v.is_empty()).unwrap_or(true)
        {
            issues.push(format!(
                "field \"{}\" is kind \"enum\" but declares no enumValues",
                field.name
            ));
        }

        if matches!(field.kind, crate::entity::FieldKind::Reference) && field.ref_entity.is_none() {
            issues.push(format!(
                "field \"{}\" is kind \"reference\" but declares no refEntity",
                field.name
            ));
        }
    }

    let is_known_field = |name: &str| -> bool { field_names.contains(name) || IMPLICIT_SYSTEM_FIELDS.contains(&name) };

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
            if transition.from.is_empty() || transition.to.is_empty() || transition.action.is_empty() {
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

        // Cross-check every state name workflow metadata mentions against `stateField`'s own
        // `enumValues` — found live (`AUDIT_2.md`): before this, a typo'd `from`/`to`/
        // `initialState`/`terminalStates` entry (e.g. `"actvie"` for `"active"`) compiled and
        // validated cleanly, then `CrudService::transition` wrote that exact typo'd value
        // straight into the record's state column with no further check
        // (`crud_service.rs`'s `to_state = transition.to.clone()`) — a permanently "broken"
        // status no other transition could ever match `from` against again. Only checkable when
        // `stateField` is actually an `Enum` with declared `enumValues` — a plain `String`
        // state field (never seen in this codebase, but not structurally forbidden) has no
        // fixed vocabulary to check against, so this degrades to a no-op rather than an error.
        let state_enum_values = entity
            .fields
            .iter()
            .find(|f| f.name == workflow.state_field)
            .filter(|f| matches!(f.kind, crate::entity::FieldKind::Enum))
            .and_then(|f| f.enum_values.as_ref());
        if let Some(enum_values) = state_enum_values {
            let enum_set: HashSet<&str> = enum_values.iter().map(String::as_str).collect();
            if !workflow.initial_state.is_empty() && !enum_set.contains(workflow.initial_state.as_str()) {
                issues.push(format!(
                    "workflow.initialState \"{}\" is not one of stateField \"{}\"'s enumValues",
                    workflow.initial_state, workflow.state_field
                ));
            }
            for state in &workflow.terminal_states {
                if !state.is_empty() && !enum_set.contains(state.as_str()) {
                    issues.push(format!(
                        "workflow.terminalStates contains \"{state}\", not one of stateField \"{}\"'s enumValues",
                        workflow.state_field
                    ));
                }
            }
            for transition in &workflow.transitions {
                if transition.from.is_empty() || transition.to.is_empty() {
                    continue; // already reported above
                }
                if !enum_set.contains(transition.from.as_str()) {
                    issues.push(format!(
                        "workflow transition \"{}\" has from-state \"{}\", not one of stateField \"{}\"'s enumValues",
                        transition.action, transition.from, workflow.state_field
                    ));
                }
                if !enum_set.contains(transition.to.as_str()) {
                    issues.push(format!(
                        "workflow transition \"{}\" has to-state \"{}\", not one of stateField \"{}\"'s enumValues",
                        transition.action, transition.to, workflow.state_field
                    ));
                }
            }
        }
    }

    // `table_name` is interpolated directly into SQL (`crates/metap-crud`, `crates/metap-query`)
    // since Postgres can't parameterize an identifier — validate it here, at the trust boundary
    // where entities are registered, rather than trusting it blindly at query time. A dedicated
    // table-per-entity table is schema-qualified (`metap_reconciler::qualified_table_name_for`,
    // e.g. `"entities.jira_issues"` — never `public`, see that function's doc comment for why),
    // so each `.`-separated segment is checked against the same identifier charset separately.
    fn is_safe_ident_segment(segment: &str) -> bool {
        !segment.is_empty()
            && segment.chars().next().is_some_and(|c| c.is_ascii_lowercase())
            && segment
                .chars()
                .all(|c| c.is_ascii_lowercase() || c.is_ascii_digit() || c == '_')
    }
    let table_name_ok = entity.table_name == "records"
        || entity.table_name.split('.').all(is_safe_ident_segment) && entity.table_name.contains('.');
    if !table_name_ok {
        issues.push(format!(
            "tableName \"{}\" must be \"records\" or a schema-qualified ^[a-z][a-z0-9_]*\\.[a-z][a-z0-9_]*$ name",
            entity.table_name
        ));
    }

    if issues.is_empty() {
        Ok(())
    } else {
        Err(MetadataValidationError {
            entity: entity.name.clone(),
            issues,
        })
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
            storage: None,
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
    fn field_name_with_unsafe_characters_is_rejected() {
        // Regression test for the SQL-injection vector `AUDIT_2.md` flagged: `field.name` is
        // interpolated directly into SQL as a column identifier
        // (`metap-reconciler::compile`) and a JSONB path literal
        // (`metap-crud::find_referencing_record`) — a name like `x") OR 1=1 --` must be
        // rejected here, at the trust boundary, since low-code-authored fields come from
        // admin-supplied metadata.
        let mut entity = minimal_entity();
        entity.fields.push(field("x\") OR 1=1 --", FieldKind::String));
        let err = validate(&entity).unwrap_err();
        assert!(err.issues.iter().any(|i| i.contains("must match")));
    }

    #[test]
    fn field_name_with_underscore_and_digits_is_allowed() {
        let mut entity = minimal_entity();
        entity.fields.push(field("field_2", FieldKind::String));
        assert!(validate(&entity).is_ok());
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

    /// Regression for the finding in `AUDIT_2.md`: a workflow transition's `from`/`to` (or
    /// `initialState`/`terminalStates`) that doesn't match any of `stateField`'s own
    /// `enumValues` — a typo, most realistically — used to validate cleanly and let
    /// `CrudService::transition` write that exact typo'd value straight into a record's state
    /// column, permanently unreachable by any other transition's `from`.
    #[test]
    fn transition_to_state_not_in_enum_values_is_rejected() {
        use crate::entity::{EntityWorkflow, WorkflowTransition};

        let mut entity = minimal_entity();
        entity.fields.push(field("status", FieldKind::Enum));
        entity.fields[1].enum_values = Some(vec!["draft".to_string(), "active".to_string()]);
        entity.workflow = Some(EntityWorkflow {
            state_field: "status".to_string(),
            initial_state: "draft".to_string(),
            terminal_states: vec![],
            transitions: vec![WorkflowTransition {
                action: "activate".to_string(),
                from: "draft".to_string(),
                to: "actvie".to_string(), // typo — not one of enumValues
                label: "Activate".to_string(),
                guard: None,
            }],
        });

        let err = validate(&entity).unwrap_err();
        assert!(err.issues.iter().any(|i| i.contains("to-state \"actvie\"")));
    }

    #[test]
    fn workflow_initial_state_not_in_enum_values_is_rejected() {
        use crate::entity::EntityWorkflow;

        let mut entity = minimal_entity();
        entity.fields.push(field("status", FieldKind::Enum));
        entity.fields[1].enum_values = Some(vec!["draft".to_string(), "active".to_string()]);
        entity.workflow = Some(EntityWorkflow {
            state_field: "status".to_string(),
            initial_state: "pending".to_string(), // not one of enumValues
            terminal_states: vec![],
            transitions: vec![],
        });

        let err = validate(&entity).unwrap_err();
        assert!(err.issues.iter().any(|i| i.contains("workflow.initialState")));
    }

    #[test]
    fn workflow_states_matching_enum_values_pass() {
        use crate::entity::{EntityWorkflow, WorkflowTransition};

        let mut entity = minimal_entity();
        entity.fields.push(field("status", FieldKind::Enum));
        entity.fields[1].enum_values = Some(vec!["draft".to_string(), "active".to_string()]);
        entity.workflow = Some(EntityWorkflow {
            state_field: "status".to_string(),
            initial_state: "draft".to_string(),
            terminal_states: vec!["active".to_string()],
            transitions: vec![WorkflowTransition {
                action: "activate".to_string(),
                from: "draft".to_string(),
                to: "active".to_string(),
                label: "Activate".to_string(),
                guard: None,
            }],
        });

        assert!(validate(&entity).is_ok());
    }

    #[test]
    fn hash_changes_when_only_a_transition_guard_changes() {
        // Regression test for `WorkflowTransition::guard`'s `#[serde(skip)]` removal
        // (2026-08-17, Phase 11 Phase B) — before that, two entities differing only by guard
        // content hashed identically, so a guard-only edit never bumped `version`.
        use crate::entity::{EntityWorkflow, WorkflowTransition};
        use metap_permission::{ConditionOp, PolicyCondition, PolicyValue};

        fn entity_with_guard(guard: Option<PolicyCondition>) -> EntityDefinition {
            let mut entity = minimal_entity();
            entity.fields.push(field("status", FieldKind::Enum));
            entity.fields[1].enum_values = Some(vec!["draft".to_string(), "active".to_string()]);
            entity.workflow = Some(EntityWorkflow {
                state_field: "status".to_string(),
                initial_state: "draft".to_string(),
                terminal_states: vec![],
                transitions: vec![WorkflowTransition {
                    action: "activate".to_string(),
                    from: "draft".to_string(),
                    to: "active".to_string(),
                    label: "Activate".to_string(),
                    guard,
                }],
            });
            entity
        }

        let no_guard = entity_with_guard(None);
        let guarded = entity_with_guard(Some(PolicyCondition::Attribute {
            attribute: "name".to_string(),
            op: ConditionOp::Neq,
            value: PolicyValue::Literal {
                literal: serde_json::json!(""),
            },
        }));
        assert_ne!(hash(&no_guard).unwrap(), hash(&guarded).unwrap());
    }
}
