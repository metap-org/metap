//! Third business entity (Phase 7, module 3/4: "workflow-heavy"). Where `sales_order_entity.rs`
//! tested a new field kind combo, this one tests a heavier workflow shape: a branch
//! (approve/reject) and a transition back out of a state that isn't the initial one
//! (`reverse`, from `posted` — a real "undo a committed action" pattern `sales.orders` doesn't
//! have). `referenceOrder` is an optional `Reference` to `sales.orders`, showing a field can
//! point at *either* other entity depending on the record — the platform doesn't care which.

use metap::permission::{ConditionOp, PolicyValue};
use metap::prelude::{
    EntityDefinition, EntityField, EntityListView, EntityWorkflow, FieldKind, PolicyCondition, WorkflowTransition,
};
use serde_json::json;

fn field(
    name: &str,
    label: &str,
    kind: FieldKind,
    required: bool,
    indexed: bool,
    searchable: bool,
    sortable: bool,
) -> EntityField {
    EntityField {
        name: name.to_string(),
        label: label.to_string(),
        kind,
        required: required.then_some(true),
        indexed: indexed.then_some(true),
        unique: None,
        enum_values: None,
        ref_entity: None,
        ref_display_field: None,
        searchable: searchable.then_some(true),
        search_mode: None,
        sortable: sortable.then_some(true),
    }
}

pub fn inventory_movement_entity() -> EntityDefinition {
    EntityDefinition {
        name: "inventory.movements".to_string(),
        label: "Inventory Movement".to_string(),
        table_name: "records".to_string(),
        fields: vec![
            field("code", "Code", FieldKind::String, true, true, true, true),
            EntityField {
                name: "movementType".to_string(),
                label: "Movement Type".to_string(),
                kind: FieldKind::Enum,
                required: Some(true),
                indexed: Some(true),
                unique: None,
                enum_values: Some(vec!["in".to_string(), "out".to_string(), "transfer".to_string()]),
                ref_entity: None,
                ref_display_field: None,
                searchable: None,
                search_mode: None,
                sortable: Some(true),
            },
            field("warehouse", "Warehouse", FieldKind::String, true, true, true, true),
            field("quantity", "Quantity", FieldKind::Number, true, false, false, true),
            EntityField {
                name: "referenceOrder".to_string(),
                label: "Reference Order".to_string(),
                kind: FieldKind::Reference,
                required: None,
                indexed: Some(true),
                unique: None,
                enum_values: None,
                ref_entity: Some("sales.orders".to_string()),
                ref_display_field: Some("code".to_string()),
                searchable: None,
                search_mode: None,
                sortable: None,
            },
            EntityField {
                name: "status".to_string(),
                label: "Status".to_string(),
                kind: FieldKind::Enum,
                required: None,
                indexed: Some(true),
                unique: None,
                enum_values: Some(vec![
                    "draft".to_string(),
                    "pending_approval".to_string(),
                    "approved".to_string(),
                    "posted".to_string(),
                    "reversed".to_string(),
                    "rejected".to_string(),
                ]),
                ref_entity: None,
                ref_display_field: None,
                searchable: None,
                search_mode: None,
                sortable: Some(true),
            },
            field("notes", "Notes", FieldKind::String, false, false, true, false),
        ],
        list_views: vec![EntityListView {
            name: "default".to_string(),
            label: "Default".to_string(),
            fields: vec![
                "code".to_string(),
                "movementType".to_string(),
                "warehouse".to_string(),
                "quantity".to_string(),
                "status".to_string(),
            ],
            filters: vec![
                "code".to_string(),
                "movementType".to_string(),
                "warehouse".to_string(),
                "status".to_string(),
            ],
            default_sort: Some("-createdAt".to_string()),
            max_limit: 100,
        }],
        workflow: Some(EntityWorkflow {
            state_field: "status".to_string(),
            initial_state: "draft".to_string(),
            // `posted` is deliberately not terminal — `reverse` transitions out of it. Only
            // `rejected`/`reversed` are true dead ends.
            terminal_states: vec!["rejected".to_string(), "reversed".to_string()],
            transitions: vec![
                WorkflowTransition {
                    action: "submit".to_string(),
                    from: "draft".to_string(),
                    to: "pending_approval".to_string(),
                    label: "Submit for approval".to_string(),
                    guard: Some(PolicyCondition::Attribute {
                        attribute: "quantity".to_string(),
                        op: ConditionOp::Neq,
                        value: PolicyValue::Literal { literal: json!(0) },
                    }),
                },
                WorkflowTransition {
                    action: "approve".to_string(),
                    from: "pending_approval".to_string(),
                    to: "approved".to_string(),
                    label: "Approve".to_string(),
                    guard: None,
                },
                WorkflowTransition {
                    action: "reject".to_string(),
                    from: "pending_approval".to_string(),
                    to: "rejected".to_string(),
                    label: "Reject".to_string(),
                    guard: None,
                },
                WorkflowTransition {
                    action: "post".to_string(),
                    from: "approved".to_string(),
                    to: "posted".to_string(),
                    label: "Post".to_string(),
                    guard: None,
                },
                WorkflowTransition {
                    action: "reverse".to_string(),
                    from: "posted".to_string(),
                    to: "reversed".to_string(),
                    label: "Reverse".to_string(),
                    guard: None,
                },
            ],
        }),
    }
}
