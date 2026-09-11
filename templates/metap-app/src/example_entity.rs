//! Starting point — replace this with your own entity. For a fuller real-world example
//! (more field kinds, a guarded transition, list-view filters), see
//! `../metap-demo-crm/src/entities/customer_entity.rs` in the metap repo.

use metap::permission::{ConditionOp, PolicyValue};
use metap::prelude::{
    submit_entity, EntityDefinition, EntityField, EntityListView, EntityWorkflow, FieldKind, PolicyCondition,
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
        storage: None,
        min: None,
        max: None,
        min_length: None,
        max_length: None,
        computed: None,
    }
}

pub fn example_entity() -> EntityDefinition {
    EntityDefinition {
        name: "example.tasks".to_string(),
        label: "Task".to_string(),
        // Standard pattern for a new app: a dedicated table in its own schema, not the shared
        // `records` table — same convention `../metap-demo-waf`/`../metap-demo-crm`/
        // `../metap-demo-jira` all converged on (`qualified_table_name_in(entity_name, schema)`,
        // `metap` repo's `crates/metap-reconciler/src/compile.rs`). Rename `"example_app"` to
        // this project's own name — `main.rs`'s boot-time `reconcile()` call creates both the
        // schema (`executor::ensure_schema_exists`, `CREATE SCHEMA IF NOT EXISTS`) and the table,
        // nothing to set up by hand first.
        table_name: metap::reconciler::qualified_table_name_in("example.tasks", "example_app"),
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
            required_fields: vec![],
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
                validator: None,
                set_fields: None,
            }],
        }),
        unique_constraints: vec![],
    }
}

submit_entity!(example_entity);
