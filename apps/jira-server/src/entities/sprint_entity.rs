//! `jira.sprints` — a project's sprint list (`docs/roadmap.md`'s jira-server demo-buildout
//! phase). Dedicated table like every other entity in this app: `jira.issues.sprint` (see
//! `issue_entity.rs`) is a `Reference` field, and `compile()` always builds that FK straight
//! into `qualified_table_name_for("jira.sprints")` regardless of whether this entity is
//! reconciled — same reasoning `project_entity.rs`'s doc comment already spells out for why
//! `jira.projects` can't stay on the shared `records` table either.
//!
//! Reconciled before `jira.issues` in `main.rs`'s boot loop (and after `jira.projects`, which
//! this entity's own `project` field references) — a `Reference`'s FK target must already be a
//! real physical table by the time the referencing table's DDL runs, so reconcile order here is
//! load-bearing, not incidental.

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
        min: None,
        max: None,
        min_length: None,
        max_length: None,
    }
}

pub fn sprint_entity() -> EntityDefinition {
    EntityDefinition {
        name: "jira.sprints".to_string(),
        label: "Sprint".to_string(),
        table_name: metap_reconciler::qualified_table_name_for("jira.sprints"),
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
                min: None,
                max: None,
                min_length: None,
                max_length: None,
            },
            field("name", "Name", FieldKind::String, true, true),
            field("goal", "Goal", FieldKind::String, false, false),
            field("startDate", "Start Date", FieldKind::Date, false, false),
            field("endDate", "End Date", FieldKind::Date, false, false),
            EntityField {
                name: "status".to_string(),
                label: "Status".to_string(),
                kind: FieldKind::Enum,
                required: None,
                indexed: Some(true),
                unique: None,
                enum_values: Some(vec![
                    "planned".to_string(),
                    "active".to_string(),
                    "completed".to_string(),
                ]),
                ref_entity: None,
                ref_display_field: None,
                searchable: None,
                search_mode: None,
                sortable: Some(true),
                storage: None,
                min: None,
                max: None,
                min_length: None,
                max_length: None,
            },
        ],
        list_views: vec![EntityListView {
            name: "default".to_string(),
            label: "Default".to_string(),
            fields: vec![
                "name".to_string(),
                "project".to_string(),
                "status".to_string(),
                "startDate".to_string(),
                "endDate".to_string(),
            ],
            filters: vec!["project".to_string(), "status".to_string()],
            default_sort: Some("-createdAt".to_string()),
            max_limit: 100,
        }],
        workflow: Some(EntityWorkflow {
            state_field: "status".to_string(),
            initial_state: "planned".to_string(),
            terminal_states: vec!["completed".to_string()],
            transitions: vec![
                WorkflowTransition {
                    action: "start".to_string(),
                    from: "planned".to_string(),
                    to: "active".to_string(),
                    label: "Start sprint".to_string(),
                    guard: None,
                    validator: None,
                    set_fields: None,
                },
                WorkflowTransition {
                    action: "complete".to_string(),
                    from: "active".to_string(),
                    to: "completed".to_string(),
                    label: "Complete sprint".to_string(),
                    guard: None,
                    validator: None,
                    set_fields: None,
                },
            ],
        }),
    }
}
