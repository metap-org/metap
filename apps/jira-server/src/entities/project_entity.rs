//! `jira.projects` — deliberately minimal (`docs/roadmap.md`'s jira-server "bước 6" scope): a
//! project is just a short `key`/`name`/`description`, no workflow. Exists mainly to give
//! `jira.issues.project` a real entity to reference — `table_name` uses a dedicated table (not
//! `records`) specifically because `metap-reconciler::compile()` builds a real FK straight into
//! `table_name_for(ref_entity)` regardless of whether that entity is actually reconciled
//! (`crates/metap-reconciler/src/compile.rs:149-151`) — if this stayed on `records`, `issue`'s
//! FK would point at a `jira_projects` table that never gets created.

use metap::prelude::{EntityDefinition, EntityField, EntityListView, FieldKind};

fn field(name: &str, label: &str, kind: FieldKind, required: bool, indexed: bool, unique: bool) -> EntityField {
    EntityField {
        name: name.to_string(),
        label: label.to_string(),
        kind,
        required: required.then_some(true),
        indexed: indexed.then_some(true),
        unique: unique.then_some(true),
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

pub fn project_entity() -> EntityDefinition {
    EntityDefinition {
        name: "jira.projects".to_string(),
        label: "Project".to_string(),
        table_name: metap_reconciler::qualified_table_name_for("jira.projects"),
        fields: vec![
            field("key", "Key", FieldKind::String, true, true, true),
            EntityField {
                // `searchable: true` (substring, `search_mode: None` defaults to ILIKE — same
                // convention `issue_entity.rs`'s `title` uses) — found live missing while
                // building the sprint-creation form's `project` `Reference` picker
                // (`ReferenceFieldInput`): it queries `?name=<partial input>` as the caller
                // types, which needs substring matching to ever return a hit for anything but
                // an exact full name.
                name: "name".to_string(),
                label: "Name".to_string(),
                kind: FieldKind::String,
                required: Some(true),
                indexed: None,
                unique: None,
                enum_values: None,
                ref_entity: None,
                ref_display_field: None,
                searchable: Some(true),
                search_mode: None,
                sortable: None,
                storage: None,
                min: None,
                max: None,
                min_length: None,
                max_length: None,
            },
            field("description", "Description", FieldKind::String, false, false, false),
        ],
        list_views: vec![EntityListView {
            name: "default".to_string(),
            label: "Default".to_string(),
            fields: vec!["key".to_string(), "name".to_string(), "description".to_string()],
            filters: vec!["key".to_string(), "name".to_string()],
            default_sort: Some("-createdAt".to_string()),
            max_limit: 100,
        }],
        workflow: None,
    }
}
