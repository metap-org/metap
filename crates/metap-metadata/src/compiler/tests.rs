use super::*;
use crate::entity::{ComputedSpec, EntityField, FieldKind};

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
        min: None,
        max: None,
        min_length: None,
        max_length: None,
        computed: None,
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
fn min_max_on_a_non_numeric_field_is_rejected() {
    let mut entity = minimal_entity();
    let mut f = field("title", FieldKind::String);
    f.min = Some(1.0);
    entity.fields.push(f);
    let err = validate(&entity).unwrap_err();
    assert!(err.issues.iter().any(|i| i.contains("declares min/max")));
}

#[test]
fn min_greater_than_max_is_rejected() {
    let mut entity = minimal_entity();
    let mut f = field("qty", FieldKind::Number);
    f.min = Some(10.0);
    f.max = Some(1.0);
    entity.fields.push(f);
    let err = validate(&entity).unwrap_err();
    assert!(err.issues.iter().any(|i| i.contains("greater than max")));
}

#[test]
fn min_max_on_a_numeric_field_passes() {
    let mut entity = minimal_entity();
    let mut f = field("qty", FieldKind::Number);
    f.min = Some(1.0);
    f.max = Some(10.0);
    entity.fields.push(f);
    assert!(validate(&entity).is_ok());
}

#[test]
fn min_length_max_length_on_a_non_string_field_is_rejected() {
    let mut entity = minimal_entity();
    let mut f = field("qty", FieldKind::Number);
    f.max_length = Some(10);
    entity.fields.push(f);
    let err = validate(&entity).unwrap_err();
    assert!(err.issues.iter().any(|i| i.contains("declares minLength/maxLength")));
}

#[test]
fn min_length_greater_than_max_length_is_rejected() {
    let mut entity = minimal_entity();
    let mut f = field("code", FieldKind::String);
    f.min_length = Some(10);
    f.max_length = Some(1);
    entity.fields.push(f);
    let err = validate(&entity).unwrap_err();
    assert!(err.issues.iter().any(|i| i.contains("greater than maxLength")));
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
            validator: None,
            set_fields: None,
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
            validator: None,
            set_fields: None,
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
                validator: None,
                set_fields: None,
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

fn computed_field(name: &str, expression: &str, depends_on: &[&str]) -> EntityField {
    let mut f = field(name, FieldKind::String);
    f.computed = Some(ComputedSpec {
        expression: expression.to_string(),
        depends_on: depends_on.iter().map(|s| s.to_string()).collect(),
    });
    f
}

#[test]
fn valid_computed_field_passes() {
    let mut entity = minimal_entity();
    entity
        .fields
        .push(computed_field("greeting", "hello {name}", &["name"]));
    assert!(validate(&entity).is_ok());
}

#[test]
fn computed_field_must_be_string_kind() {
    let mut entity = minimal_entity();
    let mut f = field("total", FieldKind::Number);
    f.computed = Some(ComputedSpec {
        expression: "{name}".to_string(),
        depends_on: vec!["name".to_string()],
    });
    entity.fields.push(f);
    let err = validate(&entity).unwrap_err();
    assert!(err.issues.iter().any(|i| i.contains("not string")));
}

#[test]
fn computed_field_cannot_be_required() {
    let mut entity = minimal_entity();
    let mut f = computed_field("greeting", "hello {name}", &["name"]);
    f.required = Some(true);
    entity.fields.push(f);
    let err = validate(&entity).unwrap_err();
    assert!(err.issues.iter().any(|i| i.contains("computed and required")));
}

#[test]
fn computed_field_cannot_be_searchable_or_sortable() {
    let mut entity = minimal_entity();
    let mut f = computed_field("greeting", "hello {name}", &["name"]);
    f.searchable = Some(true);
    entity.fields.push(f);
    let err = validate(&entity).unwrap_err();
    assert!(err.issues.iter().any(|i| i.contains("searchable/sortable")));
}

#[test]
fn computed_field_depends_on_unknown_field_is_rejected() {
    let mut entity = minimal_entity();
    entity
        .fields
        .push(computed_field("greeting", "hello {ghost}", &["ghost"]));
    let err = validate(&entity).unwrap_err();
    assert!(err.issues.iter().any(|i| i.contains("unknown field")));
}

#[test]
fn computed_field_depends_on_itself_is_rejected() {
    let mut entity = minimal_entity();
    entity
        .fields
        .push(computed_field("greeting", "{greeting}", &["greeting"]));
    let err = validate(&entity).unwrap_err();
    assert!(err.issues.iter().any(|i| i.contains("referencing itself")));
}

#[test]
fn computed_field_depends_on_another_computed_field_is_rejected() {
    let mut entity = minimal_entity();
    entity
        .fields
        .push(computed_field("greeting", "hello {name}", &["name"]));
    entity
        .fields
        .push(computed_field("banner", "{greeting}!", &["greeting"]));
    let err = validate(&entity).unwrap_err();
    assert!(err.issues.iter().any(|i| i.contains("chaining computed fields")));
}

#[test]
fn computed_field_expression_token_missing_from_depends_on_is_rejected() {
    let mut entity = minimal_entity();
    // `expression` references `{name}` but `dependsOn` doesn't list it.
    entity.fields.push(computed_field("greeting", "hello {name}", &[]));
    let err = validate(&entity).unwrap_err();
    assert!(err.issues.iter().any(|i| i.contains("not listed in dependsOn")));
}
