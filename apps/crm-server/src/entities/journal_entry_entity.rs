//! Fourth business entity (Phase 7, module 4/4: "report/export flow"). `docs/architectures/
//! 11-risks.md` already documents that there is no dedicated report/analytics query path in
//! this platform — deliberately not built ahead of a real reporting consumer. So "report" here
//! means what the platform actually has today: a second `EntityListView` (`ledger`) on the
//! same entity, filtered/sorted differently from the default operational view, proving a
//! report is just another metadata-declared list, not new backend surface. No CSV/export
//! endpoint exists or is added here.
//!
//! `post`'s guard is the first use of `PolicyCondition::Any` in this codebase (every prior
//! guard was a single `Attribute` condition) — "at least one of debit/credit is non-zero",
//! which a single condition can't express.

use metap::permission::{ConditionOp, PolicyCondition as Cond, PolicyValue};
use metap::prelude::{
    EntityDefinition, EntityField, EntityListView, EntityWorkflow, FieldKind, PolicyCondition, WorkflowTransition,
};
use serde_json::json;

fn field(
    name: &str,
    label: &str,
    kind: FieldKind,
    required: bool,
    indexed: bool,
    searchable: bool,
    sortable: bool,
) -> EntityField {
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
        sortable: sortable.then_some(true),
    }
}

fn nonzero(attribute: &str) -> PolicyCondition {
    Cond::Attribute {
        attribute: attribute.to_string(),
        op: ConditionOp::Neq,
        value: PolicyValue::Literal { literal: json!(0) },
    }
}

pub fn journal_entry_entity() -> EntityDefinition {
    EntityDefinition {
        name: "accounting.journal".to_string(),
        label: "Journal Entry".to_string(),
        table_name: "records".to_string(),
        fields: vec![
            field("code", "Code", FieldKind::String, true, true, true, true),
            field("entryDate", "Entry Date", FieldKind::Date, true, false, false, true),
            field("account", "Account", FieldKind::String, true, true, true, true),
            field("debitAmount", "Debit", FieldKind::Money, true, false, false, true),
            field("creditAmount", "Credit", FieldKind::Money, true, false, false, true),
            field(
                "description",
                "Description",
                FieldKind::String,
                false,
                false,
                true,
                false,
            ),
            EntityField {
                name: "referenceMovement".to_string(),
                label: "Reference Movement".to_string(),
                kind: FieldKind::Reference,
                required: None,
                indexed: Some(true),
                unique: None,
                enum_values: None,
                ref_entity: Some("inventory.movements".to_string()),
                ref_display_field: Some("code".to_string()),
                searchable: None,
                search_mode: None,
                sortable: None,
            },
            EntityField {
                name: "status".to_string(),
                label: "Status".to_string(),
                kind: FieldKind::Enum,
                required: None,
                indexed: Some(true),
                unique: None,
                enum_values: Some(vec!["draft".to_string(), "posted".to_string(), "voided".to_string()]),
                ref_entity: None,
                ref_display_field: None,
                searchable: None,
                search_mode: None,
                sortable: Some(true),
            },
        ],
        list_views: vec![
            EntityListView {
                name: "default".to_string(),
                label: "Default".to_string(),
                fields: vec![
                    "code".to_string(),
                    "entryDate".to_string(),
                    "account".to_string(),
                    "debitAmount".to_string(),
                    "creditAmount".to_string(),
                    "status".to_string(),
                ],
                filters: vec!["code".to_string(), "account".to_string(), "status".to_string()],
                default_sort: Some("-entryDate".to_string()),
                max_limit: 100,
            },
            EntityListView {
                name: "ledger".to_string(),
                label: "Ledger".to_string(),
                fields: vec![
                    "entryDate".to_string(),
                    "code".to_string(),
                    "account".to_string(),
                    "debitAmount".to_string(),
                    "creditAmount".to_string(),
                    "description".to_string(),
                ],
                filters: vec!["account".to_string(), "status".to_string()],
                default_sort: Some("entryDate".to_string()),
                max_limit: 100,
            },
        ],
        workflow: Some(EntityWorkflow {
            state_field: "status".to_string(),
            initial_state: "draft".to_string(),
            terminal_states: vec!["voided".to_string()],
            transitions: vec![
                WorkflowTransition {
                    action: "post".to_string(),
                    from: "draft".to_string(),
                    to: "posted".to_string(),
                    label: "Post".to_string(),
                    guard: Some(Cond::Any {
                        any: vec![nonzero("debitAmount"), nonzero("creditAmount")],
                    }),
                },
                WorkflowTransition {
                    action: "void".to_string(),
                    from: "posted".to_string(),
                    to: "voided".to_string(),
                    label: "Void".to_string(),
                    guard: None,
                },
            ],
        }),
    }
}
