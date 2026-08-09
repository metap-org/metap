//! Starting point — replace this with your own entity. For a fuller real-world example
//! (more field kinds, a guarded transition, list-view filters), see
//! `apps/crm-server/src/customer_entity.rs` in the metap repo.

use metap::permission::{ConditionOp, PolicyValue};
use metap::prelude::{
    EntityDefinition, EntityField, EntityListView, EntityWorkflow, FieldKind, PolicyCondition,
    WorkflowTransition,
};

fn field(name: &str, label: &str, kind: FieldKind) -> EntityField {
    EntityField {
        name: name.to_string(),
        label: label.to_string(),
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

pub fn example_entity() -> EntityDefinition {
    EntityDefinition {
        name: "example.tasks".to_string(),
        label: "Task".to_string(),
        table_name: "records".to_string(),
        fields: vec![
            EntityField {
                required: Some(true),
                sortable: Some(true),
                searchable: Some(true),
                ..field("title", "Title", FieldKind::String)
            },
            EntityField {
                enum_values: Some(vec!["draft".to_string(), "done".to_string()]),
                ..field("status", "Status", FieldKind::Enum)
            },
        ],
        list_views: vec![EntityListView {
            name: "default".to_string(),
            label: "Default".to_string(),
            fields: vec!["title".to_string(), "status".to_string()],
            filters: vec![],
            default_sort: Some("-createdAt".to_string()),
            max_limit: 50,
        }],
        workflow: Some(EntityWorkflow {
            state_field: "status".to_string(),
            initial_state: "draft".to_string(),
            terminal_states: vec!["done".to_string()],
            transitions: vec![WorkflowTransition {
                action: "complete".to_string(),
                from: "draft".to_string(),
                to: "done".to_string(),
                label: "Complete".to_string(),
                // Guards are a `PolicyCondition`, not a function — this one requires a
                // non-empty title before the task can be marked done.
                guard: Some(PolicyCondition::Attribute {
                    attribute: "title".to_string(),
                    op: ConditionOp::Neq,
                    value: PolicyValue::Literal { literal: serde_json::json!("") },
                }),
            }],
        }),
    }
}
