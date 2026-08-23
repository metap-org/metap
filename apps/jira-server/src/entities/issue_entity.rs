//! `jira.issues` — the entity this whole app exists to prove out: a dedicated table
//! (`table_name != "records"`), reconciled at boot (`src/main.rs`), with a real FK-constrained
//! `project` column (a `Reference` field always gets a real column under table-per-entity, see
//! `crates/metap-metadata/src/entity.rs`'s `field_has_real_column`).
//!
//! `assigneeEmail`/`reporterEmail` are plain text, not `Reference` fields — `users` is a
//! platform/auth table, not a registered `EntityDefinition`, so there's nothing for a
//! `Reference`'s `refEntity` to point at. Kept deliberately simple for this PoC's scope.

use metap::permission::{ConditionOp, PolicyValue};
use metap::prelude::{EntityDefinition, EntityField, EntityListView, EntityWorkflow, FieldKind, WorkflowTransition};
use serde_json::json;

fn field(name: &str, label: &str, kind: FieldKind, required: bool, indexed: bool, searchable: bool) -> EntityField {
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
        sortable: None,
        storage: None,
    }
}

pub fn issue_entity() -> EntityDefinition {
    EntityDefinition {
        name: "jira.issues".to_string(),
        label: "Issue".to_string(),
        table_name: metap_reconciler::qualified_table_name_for("jira.issues"),
        fields: vec![
            field("title", "Title", FieldKind::String, true, false, true),
            field("description", "Description", FieldKind::String, false, false, false),
            EntityField {
                name: "priority".to_string(),
                label: "Priority".to_string(),
                kind: FieldKind::Enum,
                required: Some(true),
                indexed: Some(true),
                unique: None,
                enum_values: Some(vec![
                    "low".to_string(),
                    "medium".to_string(),
                    "high".to_string(),
                    "urgent".to_string(),
                ]),
                ref_entity: None,
                ref_display_field: None,
                searchable: None,
                search_mode: None,
                sortable: Some(true),
                storage: None,
            },
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
            EntityField {
                name: "sprint".to_string(),
                label: "Sprint".to_string(),
                kind: FieldKind::Reference,
                // Optional — an issue with no sprint sits in the backlog, same convention a
                // real Jira board uses for its "Backlog" column/swimlane.
                required: None,
                indexed: None,
                unique: None,
                enum_values: None,
                ref_entity: Some("jira.sprints".to_string()),
                ref_display_field: Some("name".to_string()),
                searchable: None,
                search_mode: None,
                sortable: None,
                storage: None,
            },
            field(
                "assigneeEmail",
                "Assignee Email",
                FieldKind::String,
                false,
                false,
                false,
            ),
            field("reporterEmail", "Reporter Email", FieldKind::String, true, false, false),
            field("dueDate", "Due Date", FieldKind::Date, false, true, false),
            EntityField {
                name: "status".to_string(),
                label: "Status".to_string(),
                kind: FieldKind::Enum,
                required: None,
                indexed: Some(true),
                unique: None,
                enum_values: Some(vec![
                    "todo".to_string(),
                    "in_progress".to_string(),
                    "in_review".to_string(),
                    "done".to_string(),
                ]),
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
            fields: vec![
                "title".to_string(),
                "priority".to_string(),
                "project".to_string(),
                "sprint".to_string(),
                "status".to_string(),
                "assigneeEmail".to_string(),
                "dueDate".to_string(),
            ],
            filters: vec![
                "title".to_string(),
                "priority".to_string(),
                "project".to_string(),
                "sprint".to_string(),
                "status".to_string(),
            ],
            default_sort: Some("-createdAt".to_string()),
            max_limit: 100,
        }],
        // 4 states so the kanban board (`apps/jira-fe`) has a genuine review column, not just
        // "doing"/"done" — `start`/`submit_for_review`/`request_changes`/`approve`/`reopen`
        // together form a diamond (`in_review` can go either back to `in_progress` or forward to
        // `done`), which is exactly the shape a board's drag-and-drop needs to call the right
        // transition action per source/target column pair.
        workflow: Some(EntityWorkflow {
            state_field: "status".to_string(),
            initial_state: "todo".to_string(),
            terminal_states: vec!["done".to_string()],
            transitions: vec![
                WorkflowTransition {
                    action: "start".to_string(),
                    from: "todo".to_string(),
                    to: "in_progress".to_string(),
                    label: "Start work".to_string(),
                    guard: None,
                },
                WorkflowTransition {
                    action: "submit_for_review".to_string(),
                    from: "in_progress".to_string(),
                    to: "in_review".to_string(),
                    label: "Submit for review".to_string(),
                    guard: None,
                },
                WorkflowTransition {
                    action: "request_changes".to_string(),
                    from: "in_review".to_string(),
                    to: "in_progress".to_string(),
                    label: "Request changes".to_string(),
                    guard: None,
                },
                WorkflowTransition {
                    action: "approve".to_string(),
                    from: "in_review".to_string(),
                    to: "done".to_string(),
                    label: "Approve".to_string(),
                    guard: Some(metap::permission::PolicyCondition::Attribute {
                        attribute: "reporterEmail".to_string(),
                        op: ConditionOp::Neq,
                        value: PolicyValue::Literal { literal: json!("") },
                    }),
                },
                WorkflowTransition {
                    action: "reopen".to_string(),
                    from: "done".to_string(),
                    to: "todo".to_string(),
                    label: "Reopen".to_string(),
                    guard: None,
                },
            ],
        }),
    }
}
