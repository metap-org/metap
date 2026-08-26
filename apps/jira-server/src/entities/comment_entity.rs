//! `jira.comments` — a flat comment thread per issue (`docs/roadmap.md`'s jira-server
//! demo-buildout phase). No `authorEmail`-as-`Reference` for the same reason
//! `issue_entity.rs`'s `assigneeEmail`/`reporterEmail` aren't: `users` isn't a registered
//! `EntityDefinition`. No workflow — a comment has no lifecycle beyond existing/deleted.
//!
//! Reconciled last in `main.rs`'s boot loop — `issue` is a `Reference` into `jira.issues`, whose
//! table must already exist by the time this entity's FK-creating DDL runs (see
//! `sprint_entity.rs`'s doc comment for why reconcile order is load-bearing here, not
//! incidental).

use metap::prelude::{EntityDefinition, EntityField, EntityListView, FieldKind};

pub fn comment_entity() -> EntityDefinition {
    EntityDefinition {
        name: "jira.comments".to_string(),
        label: "Comment".to_string(),
        table_name: metap_reconciler::qualified_table_name_for("jira.comments"),
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
                min: None,
                max: None,
                min_length: None,
                max_length: None,
            },
            EntityField {
                name: "authorEmail".to_string(),
                label: "Author Email".to_string(),
                kind: FieldKind::String,
                required: Some(true),
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
            },
            EntityField {
                name: "body".to_string(),
                label: "Body".to_string(),
                kind: FieldKind::String,
                required: Some(true),
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
            },
        ],
        list_views: vec![EntityListView {
            name: "default".to_string(),
            label: "Default".to_string(),
            fields: vec!["issue".to_string(), "authorEmail".to_string(), "body".to_string()],
            filters: vec!["issue".to_string()],
            default_sort: Some("createdAt".to_string()),
            max_limit: 200,
        }],
        workflow: None,
    }
}
