//! Rust port of `apps/crm/src/modules/crm/customer.entity.ts` — a demo of the full stack
//! (`crates/metap-*`) serving the repo's actual real business entity, not a synthetic
//! `test.*` fixture. Not part of `docs/rust-core-viability.md`'s Migration Order (that
//! document names porting a real entity as explicitly out of scope); this exists to
//! demonstrate the finished stack end to end.
//!
//! `activate`'s guard (TS: `typeof data.email === "string" && data.email.length > 0`) has
//! no exact equivalent in `PolicyCondition`'s operator set (eq/neq/in/notIn, no "non-empty
//! string" predicate) — approximated here as `email != ""`, matching the guard's practical
//! intent (an email must be present) without the type-check TS's predicate function could
//! express directly.

use metap_metadata::{
    EntityDefinition, EntityField, EntityListView, EntityWorkflow, FieldKind, WorkflowTransition,
};
use metap_permission::{ConditionOp, PolicyCondition, PolicyValue};
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

pub fn customer_entity() -> EntityDefinition {
    EntityDefinition {
        name: "crm.customers".to_string(),
        label: "Customer".to_string(),
        table_name: "records".to_string(),
        fields: vec![
            field("code", "Code", FieldKind::String, true, true, true, true),
            field("name", "Name", FieldKind::String, true, false, true, true),
            field("phone", "Phone", FieldKind::String, false, false, true, false),
            field("email", "Email", FieldKind::String, false, false, true, false),
            EntityField {
                name: "status".to_string(),
                label: "Status".to_string(),
                kind: FieldKind::Enum,
                required: None,
                indexed: Some(true),
                unique: None,
                enum_values: Some(vec!["draft".to_string(), "active".to_string(), "blocked".to_string()]),
                ref_entity: None,
                ref_display_field: None,
                searchable: None,
                search_mode: None,
                sortable: Some(true),
            },
            EntityField {
                name: "referredBy".to_string(),
                label: "Referred By".to_string(),
                kind: FieldKind::Reference,
                required: None,
                indexed: None,
                unique: None,
                enum_values: None,
                ref_entity: Some("crm.customers".to_string()),
                ref_display_field: Some("name".to_string()),
                searchable: None,
                search_mode: None,
                sortable: None,
            },
        ],
        list_views: vec![EntityListView {
            name: "default".to_string(),
            label: "Default".to_string(),
            fields: vec![
                "code".to_string(),
                "name".to_string(),
                "phone".to_string(),
                "email".to_string(),
                "status".to_string(),
            ],
            filters: vec![
                "code".to_string(),
                "name".to_string(),
                "phone".to_string(),
                "email".to_string(),
                "status".to_string(),
            ],
            default_sort: Some("-createdAt".to_string()),
            max_limit: 100,
        }],
        workflow: Some(EntityWorkflow {
            state_field: "status".to_string(),
            initial_state: "draft".to_string(),
            terminal_states: vec!["blocked".to_string()],
            transitions: vec![
                WorkflowTransition {
                    action: "activate".to_string(),
                    from: "draft".to_string(),
                    to: "active".to_string(),
                    label: "Activate".to_string(),
                    guard: Some(PolicyCondition::Attribute {
                        attribute: "email".to_string(),
                        op: ConditionOp::Neq,
                        value: PolicyValue::Literal { literal: json!("") },
                    }),
                },
                WorkflowTransition {
                    action: "block".to_string(),
                    from: "active".to_string(),
                    to: "blocked".to_string(),
                    label: "Block".to_string(),
                    guard: None,
                },
            ],
        }),
    }
}
