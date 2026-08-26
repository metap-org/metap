//! `jira.epics` — a project's epic list, groups issues above sprints (a sprint is time-boxed, an
//! epic is a larger body of work spanning many sprints). Same shape as `sprint_entity.rs`:
//! dedicated table, `Reference` into `jira.projects`, own small workflow.
//!
//! Reconciled before `jira.issues` in `main.rs`'s boot loop (and after `jira.projects`) — same
//! FK-target-must-already-exist ordering constraint `sprint_entity.rs`'s doc comment explains.

use metap::prelude::{EntityDefinition, EntityField, EntityListView, EntityWorkflow, FieldKind, WorkflowTransition};

fn field(name: &str, label: &str, kind: FieldKind, required: bool, indexed: bool) -> EntityField {
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
        searchable: None,
        search_mode: None,
        sortable: None,
        storage: None,
    }
}

pub fn epic_entity() -> EntityDefinition {
    EntityDefinition {
        name: "jira.epics".to_string(),
        label: "Epic".to_string(),
        table_name: metap_reconciler::qualified_table_name_for("jira.epics"),
        fields: vec![
            EntityField {
                name: "project".to_string(),
                label: "Project".to_string(),
                kind: FieldKind::Reference,
                required: Some(true),
                indexed: None,
                unique: None,
                enum_values: None,
                ref_entity: Some("jira.projects".to_string()),
                ref_display_field: Some("name".to_string()),
                searchable: None,
                search_mode: None,
                sortable: None,
                storage: None,
            },
            field("name", "Name", FieldKind::String, true, true),
            field("description", "Description", FieldKind::String, false, false),
            EntityField {
                name: "status".to_string(),
                label: "Status".to_string(),
                kind: FieldKind::Enum,
                required: None,
                indexed: Some(true),
                unique: None,
                enum_values: Some(vec!["open".to_string(), "closed".to_string()]),
                ref_entity: None,
                ref_display_field: None,
                searchable: None,
                search_mode: None,
                sortable: Some(true),
                storage: None,
            },
        ],
        list_views: vec![EntityListView {
            name: "default".to_string(),
            label: "Default".to_string(),
            fields: vec!["name".to_string(), "project".to_string(), "status".to_string()],
            filters: vec!["project".to_string(), "status".to_string()],
            default_sort: Some("-createdAt".to_string()),
            max_limit: 100,
        }],
        workflow: Some(EntityWorkflow {
            state_field: "status".to_string(),
            initial_state: "open".to_string(),
            terminal_states: vec!["closed".to_string()],
            transitions: vec![WorkflowTransition {
                action: "close".to_string(),
                from: "open".to_string(),
                to: "closed".to_string(),
                label: "Close epic".to_string(),
                guard: None,
                validator: None,
                set_fields: None,
            }],
        }),
    }
}
