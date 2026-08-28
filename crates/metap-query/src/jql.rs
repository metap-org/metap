//! A small, generic query language ("JQL", after Jira's) any entity can use through
//! `ListInput.jql` — `field OP value` comparisons combined with `AND`/`OR`/`NOT`, e.g.
//! `priority = "high" AND status != "done" ORDER BY dueDate DESC`. Lives in `metap-query`
//! (the crate that already owns `plan_list`/`QueryPlanner`, this project's one deliberate
//! Postgres-dialect seam — see this crate's top doc comment) rather than any app — nothing
//! here is jira-specific, it only ever sees the calling entity's own `EntityDefinition`.
//!
//! Same security posture the boundary in `docs/architectures/05-building-blocks.md` requires of
//! every other filter path: **field names are validated against `entity.fields`** (never an
//! arbitrary client-supplied column/JSON path), **operators are a fixed allowlist** gated by the
//! field's `FieldKind` (no `>`/`<` on a `String`, no `~` on a `Boolean`), and **every value is
//! bound as a SQL parameter** (`ParamBuilder`/`BindValue`) — nothing from the query text is ever
//! interpolated directly into the generated SQL string. A malformed or disallowed query is a
//! clean `InvalidJqlError` (→ HTTP 400 via `CrudService::list`'s downcast, same pattern as
//! `InvalidCursorError`/`UnknownListViewError`), never a panic or a raw DB error.
//!
//! Deliberately smaller than real JQL: string/number/`true`/`false` literals only (no bare
//! unquoted words — `status = Done` isn't valid, write `status = "Done"`; keeps the lexer from
//! having to disambiguate a bare value from a field name), and `ORDER BY` takes exactly one
//! field — the rest of this codebase (cursor pagination, `plan_list`'s `ResolvedSort`) only
//! ever models a single sort column, so a multi-column `ORDER BY` would be accepted here and
//! silently unusable everywhere else.
//!
//! Split into one file per compiler stage (`lexer` -> `parser` -> `ast` in between -> `codegen`)
//! purely to keep each file a manageable size — `parse_and_compile_jql`/`InvalidJqlError` are
//! the only two items anything outside this module ever names, and both are re-exported here
//! unchanged.

mod ast;
mod codegen;
mod lexer;
mod parser;

pub use codegen::{parse_and_compile_jql, JqlCompileResult};
pub use lexer::InvalidJqlError;

#[cfg(test)]
mod tests {
    use metap_metadata::{EntityDefinition, EntityField, FieldKind};

    use crate::sql_builder::ParamBuilder;

    use super::{parse_and_compile_jql, JqlCompileResult};

    fn test_entity() -> EntityDefinition {
        EntityDefinition {
            name: "jira.issues".to_string(),
            label: "Issue".to_string(),
            table_name: "records".to_string(),
            fields: vec![
                EntityField {
                    name: "priority".to_string(),
                    label: "Priority".to_string(),
                    kind: FieldKind::Enum,
                    required: None,
                    indexed: None,
                    unique: None,
                    enum_values: Some(vec!["low".to_string(), "high".to_string()]),
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
                EntityField {
                    name: "storyPoints".to_string(),
                    label: "Story Points".to_string(),
                    kind: FieldKind::Number,
                    required: None,
                    indexed: None,
                    unique: None,
                    enum_values: None,
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
                EntityField {
                    name: "title".to_string(),
                    label: "Title".to_string(),
                    kind: FieldKind::String,
                    required: None,
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
            ],
            list_views: vec![],
            workflow: None,
        }
    }

    fn compile(jql: &str) -> JqlCompileResult {
        let entity = test_entity();
        let mut params = ParamBuilder::new();
        parse_and_compile_jql(jql, &entity, false, &mut params)
    }

    #[test]
    fn simple_equality_compiles_and_binds_one_param() {
        let entity = test_entity();
        let mut params = ParamBuilder::new();
        let (sql, order) = parse_and_compile_jql("priority = \"high\"", &entity, false, &mut params).unwrap();
        assert!(!sql.unwrap().contains("ILIKE"));
        assert!(order.is_none());
        assert_eq!(params.params.len(), 2); // jsonb key placeholder + value placeholder
    }

    #[test]
    fn and_or_not_precedence_and_parens_all_parse() {
        assert!(compile("priority = \"high\" AND storyPoints > 3").unwrap().0.is_some());
        assert!(compile("priority = \"high\" OR priority = \"low\"")
            .unwrap()
            .0
            .is_some());
        assert!(compile("NOT priority = \"high\"").unwrap().0.is_some());
        assert!(
            compile("(priority = \"high\" OR priority = \"low\") AND storyPoints >= 1")
                .unwrap()
                .0
                .is_some()
        );
    }

    #[test]
    fn in_and_is_empty_compile() {
        assert!(compile("priority IN (\"high\", \"low\")").unwrap().0.is_some());
        assert!(compile("priority NOT IN (\"high\")").unwrap().0.is_some());
        assert!(compile("storyPoints IS EMPTY").unwrap().0.is_some());
        assert!(compile("storyPoints IS NOT EMPTY").unwrap().0.is_some());
    }

    #[test]
    fn contains_operator_works_on_text_fields() {
        assert!(compile("title ~ \"kanban\"").unwrap().0.is_some());
    }

    #[test]
    fn order_by_compiles_to_field_and_direction() {
        let (_, order) = compile("priority = \"high\" ORDER BY storyPoints DESC").unwrap();
        assert_eq!(order, Some(("storyPoints".to_string(), true)));
    }

    #[test]
    fn order_by_only_with_no_filter_expression_is_allowed() {
        let (where_sql, order) = compile("ORDER BY storyPoints ASC").unwrap();
        assert!(where_sql.is_none());
        assert_eq!(order, Some(("storyPoints".to_string(), false)));
    }

    #[test]
    fn unknown_field_is_a_clean_error_not_a_panic() {
        let err = compile("nope = \"x\"").unwrap_err();
        assert!(err.0.contains("Unknown field"));
    }

    #[test]
    fn range_operator_rejected_on_non_orderable_kind() {
        let err = compile("priority > \"high\"").unwrap_err();
        assert!(err.0.contains("not supported"));
    }

    #[test]
    fn order_by_on_a_non_sortable_field_is_rejected() {
        let err = compile("title = \"x\" ORDER BY title").unwrap_err();
        assert!(err.0.contains("not sortable"));
    }

    #[test]
    fn unterminated_string_and_trailing_garbage_are_clean_errors() {
        assert!(compile("priority = \"high").is_err());
        assert!(compile("priority = \"high\" wat").is_err());
    }
}
