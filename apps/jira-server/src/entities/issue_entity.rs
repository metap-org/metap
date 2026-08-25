//! `jira.issues` — the entity this whole app exists to prove out: a dedicated table
//! (`table_name != "records"`), reconciled at boot (`src/main.rs`), with a real FK-constrained
//! `project` column (a `Reference` field always gets a real column under table-per-entity, see
//! `crates/metap-metadata/src/entity.rs`'s `field_has_real_column`).
//!
//! `assigneeEmail`/`reporterEmail` are plain text, not `Reference` fields — `users` is a
//! platform/auth table, not a registered `EntityDefinition`, so there's nothing for a
//! `Reference`'s `refEntity` to point at. Kept deliberately simple for this PoC's scope.
//!
//! `parentIssue` is a **self-referencing** `Reference` (`ref_entity == "jira.issues"`, this
//! entity's own name) — sub-tasks. Worth calling out because it's a genuinely different case
//! from every other `Reference` field in this app: `metap-reconciler::compile()`'s FK-target-
//! must-already-exist ordering constraint (see `sprint_entity.rs`'s doc comment) is trivially
//! satisfied here since the table is reconciled before its own FK constraint is ever attempted
//! within the same `CREATE TABLE`, but it's still the first self-reference this whole codebase
//! has ever declared — verified live it reconciles and round-trips correctly, not assumed.
//!
//! `labels` uses `FieldKind::Json` (a bare string array, e.g. `["bug","urgent"]`) — metap has no
//! dedicated multi-select/tag `FieldKind` yet, and `Json` is the closest existing fit. Known
//! rough edge, not silently smoothed over: `packages/platform-react`'s generic `FieldInput`
//! renders any `Json` field as a raw-text `Textarea` (`case "json"` in that file), so editing
//! labels through the generic form means typing `["bug","urgent"]` by hand — a real gap a
//! first-class tag/multi-select `FieldKind` would close, not built here since one entity's ad
//! hoc need isn't a strong enough trigger for a new platform-wide field kind on its own.

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
            EntityField {
                name: "epic".to_string(),
                label: "Epic".to_string(),
                kind: FieldKind::Reference,
                // Optional — not every issue belongs to an epic.
                required: None,
                indexed: None,
                unique: None,
                enum_values: None,
                ref_entity: Some("jira.epics".to_string()),
                ref_display_field: Some("name".to_string()),
                searchable: None,
                search_mode: None,
                sortable: None,
                storage: None,
            },
            EntityField {
                name: "issueType".to_string(),
                label: "Issue Type".to_string(),
                kind: FieldKind::Enum,
                required: Some(true),
                indexed: Some(true),
                unique: None,
                enum_values: Some(vec!["bug".to_string(), "task".to_string(), "story".to_string()]),
                ref_entity: None,
                ref_display_field: None,
                searchable: None,
                search_mode: None,
                sortable: Some(true),
                storage: None,
            },
            EntityField {
                name: "parentIssue".to_string(),
                label: "Parent Issue".to_string(),
                kind: FieldKind::Reference,
                // Optional — an issue with no parent is a top-level issue, not a sub-task.
                required: None,
                indexed: None,
                unique: None,
                enum_values: None,
                ref_entity: Some("jira.issues".to_string()),
                ref_display_field: Some("title".to_string()),
                searchable: None,
                search_mode: None,
                sortable: None,
                storage: None,
            },
            field("storyPoints", "Story Points", FieldKind::Number, false, false, false),
            field(
                "originalEstimateMinutes",
                "Original Estimate (minutes)",
                FieldKind::Number,
                false,
                false,
                false,
            ),
            field("labels", "Labels", FieldKind::Json, false, false, false),
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
                "issueType".to_string(),
                "priority".to_string(),
                "project".to_string(),
                "sprint".to_string(),
                "epic".to_string(),
                "status".to_string(),
                "assigneeEmail".to_string(),
                "dueDate".to_string(),
                "storyPoints".to_string(),
            ],
            filters: vec![
                "title".to_string(),
                "issueType".to_string(),
                "priority".to_string(),
                "project".to_string(),
                "sprint".to_string(),
                "epic".to_string(),
                "status".to_string(),
                // Lets a sub-task panel filter "?parentIssue={id}" the same way
                // `jira.comments`'s `filters: ["issue"]` already does for its parent — same
                // pattern, not a new capability.
                "parentIssue".to_string(),
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
