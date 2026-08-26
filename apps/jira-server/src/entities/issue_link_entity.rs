//! `jira.issue_links` — a typed, symmetric relationship between two issues ("blocks",
//! "relates to", "duplicates"), distinct from `parentIssue`'s parent/sub-task hierarchy.
//! Genuinely different shape from every other `Reference` field in this app: **two** `Reference`
//! fields on the same entity both pointing at the same target (`jira.issues`) — worth building
//! specifically to see whether `metap-reconciler::compile()` handles two FK columns into the
//! same target table on one entity without the constraint/index names colliding.
//!
//! Reconciled last in `main.rs`'s boot loop — both `fromIssue`/`toIssue` need `jira.issues`'s
//! table to already exist.

use metap::prelude::{EntityDefinition, EntityField, EntityListView, FieldKind};

fn issue_ref(name: &str, label: &str) -> EntityField {
    EntityField {
        name: name.to_string(),
        label: label.to_string(),
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
    }
}

pub fn issue_link_entity() -> EntityDefinition {
    EntityDefinition {
        name: "jira.issue_links".to_string(),
        label: "Issue Link".to_string(),
        table_name: metap_reconciler::qualified_table_name_for("jira.issue_links"),
        fields: vec![
            issue_ref("fromIssue", "From Issue"),
            issue_ref("toIssue", "To Issue"),
            EntityField {
                name: "linkType".to_string(),
                label: "Link Type".to_string(),
                kind: FieldKind::Enum,
                required: Some(true),
                indexed: Some(true),
                unique: None,
                enum_values: Some(vec![
                    "relates_to".to_string(),
                    "blocks".to_string(),
                    "duplicates".to_string(),
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
            fields: vec!["fromIssue".to_string(), "toIssue".to_string(), "linkType".to_string()],
            // Both directions filterable — a link's "from" side wants links where it's the
            // source, the "to" side (shown on the other issue's page) wants the reverse.
            filters: vec!["fromIssue".to_string(), "toIssue".to_string()],
            default_sort: Some("-createdAt".to_string()),
            max_limit: 100,
        }],
        workflow: None,
    }
}
