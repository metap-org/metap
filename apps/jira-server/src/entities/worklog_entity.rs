//! `jira.worklogs` — time tracking entries against an issue ("logwork"). Compared against
//! `jira.issues.originalEstimateMinutes` (see `issue_entity.rs`) to show estimate-vs-logged, the
//! same "original estimate" / "time spent" pair real Jira tracks.
//!
//! Reconciled after `jira.issues` in `main.rs`'s boot loop — `issue` is a `Reference` into it.

use metap::prelude::{EntityDefinition, EntityField, EntityListView, FieldKind};

fn field(name: &str, label: &str, kind: FieldKind, required: bool, sortable: bool) -> EntityField {
    EntityField {
        name: name.to_string(),
        label: label.to_string(),
        kind,
        required: required.then_some(true),
        indexed: None,
        unique: None,
        enum_values: None,
        ref_entity: None,
        ref_display_field: None,
        searchable: None,
        search_mode: None,
        sortable: sortable.then_some(true),
        storage: None,
    }
}

pub fn worklog_entity() -> EntityDefinition {
    EntityDefinition {
        name: "jira.worklogs".to_string(),
        label: "Worklog".to_string(),
        table_name: metap_reconciler::qualified_table_name_for("jira.worklogs"),
        fields: vec![
            EntityField {
                name: "issue".to_string(),
                label: "Issue".to_string(),
                kind: FieldKind::Reference,
                required: Some(true),
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
            field("authorEmail", "Author Email", FieldKind::String, true, false),
            field(
                "timeSpentMinutes",
                "Time Spent (minutes)",
                FieldKind::Number,
                true,
                false,
            ),
            field("workDate", "Work Date", FieldKind::Date, true, true),
            field("description", "Description", FieldKind::String, false, false),
        ],
        list_views: vec![EntityListView {
            name: "default".to_string(),
            label: "Default".to_string(),
            fields: vec![
                "issue".to_string(),
                "authorEmail".to_string(),
                "timeSpentMinutes".to_string(),
                "workDate".to_string(),
            ],
            filters: vec!["issue".to_string()],
            default_sort: Some("-workDate".to_string()),
            max_limit: 200,
        }],
        workflow: None,
    }
}
