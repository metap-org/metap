//! Second business entity registered in this binary — a transaction module (`sales.orders`),
//! not master data like `crm.customers`. Exists to test whether the metadata-driven pattern
//! (Phase 7, `docs/roadmap.md`) generalizes past a single entity: a `Reference` field pointing
//! at another entity, a `Money`/`Date` field kind, and a workflow with more than two states.
//!
//! `confirm`'s guard (`customer != ""`) is redundant with `customer` already being
//! `required: true` at payload-validation time — kept anyway, mirroring `customer_entity.rs`'s
//! `activate` guard, to demonstrate a workflow guard on a `Reference` field specifically.

use metap::permission::{ConditionOp, PolicyValue};
use metap::prelude::{
    EntityDefinition, EntityField, EntityListView, EntityWorkflow, FieldKind, PolicyCondition,
    WorkflowTransition,
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

pub fn sales_order_entity() -> EntityDefinition {
    EntityDefinition {
        name: "sales.orders".to_string(),
        label: "Sales Order".to_string(),
        table_name: "records".to_string(),
        fields: vec![
            field("code", "Code", FieldKind::String, true, true, true, true),
            EntityField {
                name: "customer".to_string(),
                label: "Customer".to_string(),
                kind: FieldKind::Reference,
                required: Some(true),
                indexed: Some(true),
                unique: None,
                enum_values: None,
                ref_entity: Some("crm.customers".to_string()),
                ref_display_field: Some("name".to_string()),
                searchable: None,
                search_mode: None,
                sortable: None,
            },
            field("orderDate", "Order Date", FieldKind::Date, true, false, false, true),
            field("totalAmount", "Total Amount", FieldKind::Money, true, false, false, true),
            EntityField {
                name: "status".to_string(),
                label: "Status".to_string(),
                kind: FieldKind::Enum,
                required: None,
                indexed: Some(true),
                unique: None,
                enum_values: Some(vec![
                    "draft".to_string(),
                    "confirmed".to_string(),
                    "shipped".to_string(),
                    "cancelled".to_string(),
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
                "customer".to_string(),
                "orderDate".to_string(),
                "totalAmount".to_string(),
                "status".to_string(),
            ],
            filters: vec!["code".to_string(), "customer".to_string(), "status".to_string()],
            default_sort: Some("-orderDate".to_string()),
            max_limit: 100,
        }],
        workflow: Some(EntityWorkflow {
            state_field: "status".to_string(),
            initial_state: "draft".to_string(),
            terminal_states: vec!["shipped".to_string(), "cancelled".to_string()],
            transitions: vec![
                WorkflowTransition {
                    action: "confirm".to_string(),
                    from: "draft".to_string(),
                    to: "confirmed".to_string(),
                    label: "Confirm".to_string(),
                    guard: Some(PolicyCondition::Attribute {
                        attribute: "customer".to_string(),
                        op: ConditionOp::Neq,
                        value: PolicyValue::Literal { literal: json!("") },
                    }),
                },
                WorkflowTransition {
                    action: "ship".to_string(),
                    from: "confirmed".to_string(),
                    to: "shipped".to_string(),
                    label: "Ship".to_string(),
                    guard: None,
                },
                WorkflowTransition {
                    action: "cancel".to_string(),
                    from: "draft".to_string(),
                    to: "cancelled".to_string(),
                    label: "Cancel".to_string(),
                    guard: None,
                },
            ],
        }),
    }
}
