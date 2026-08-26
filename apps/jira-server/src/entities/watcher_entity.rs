//! `jira.watchers` — who's subscribed to an issue. Only the subscription list itself; there is
//! no notification *delivery* mechanism built on top of it here (`notification-worker` logs
//! workflow transitions to stdout only — no email/webhook integration exists anywhere in this
//! repo yet, see that crate's doc comment — wiring an actual "notify every watcher" delivery
//! path is a separate, larger piece of platform infrastructure, not built as a side effect of
//! this entity).
//!
//! No composite-unique constraint on `(issue, userEmail)` — `EntityField.unique` is a single-
//! field flag, `EntityDefinition` has no composite-unique concept today. A user could technically
//! end up double-watching the same issue through a race; the FE checks before creating, but this
//! isn't DB-enforced. Not worth a new metadata capability for one low-stakes entity.
//!
//! Reconciled after `jira.issues` in `main.rs`'s boot loop — `issue` is a `Reference` into it.

use metap::prelude::{EntityDefinition, EntityField, EntityListView, FieldKind};

pub fn watcher_entity() -> EntityDefinition {
    EntityDefinition {
        name: "jira.watchers".to_string(),
        label: "Watcher".to_string(),
        table_name: metap_reconciler::qualified_table_name_for("jira.watchers"),
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
                name: "userEmail".to_string(),
                label: "User Email".to_string(),
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
            fields: vec!["issue".to_string(), "userEmail".to_string()],
            filters: vec!["issue".to_string(), "userEmail".to_string()],
            default_sort: Some("-createdAt".to_string()),
            max_limit: 100,
        }],
        workflow: None,
    }
}
