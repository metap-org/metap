//! Tiny SQL-quoting helpers shared by `executor` and `backfill`. DDL statements do not support
//! bind parameters at all (unlike a `SELECT`), so identifiers/literals built from metadata have
//! to be quoted and inlined — safe only because they come exclusively from server-authored,
//! `MetadataCompiler`-validated metadata (never request input), same reasoning as
//! `crates/metap-peripherals/src/index_reconciler.rs`'s `quote_identifier`/`quote_literal`.

pub fn quote_ident(name: &str) -> String {
    format!("\"{}\"", name.replace('"', "\"\""))
}

/// Every physical table this crate manages lives in a schema-qualified name
/// (`crate::compile::qualified_table_name_for`, e.g. `"entities.jira_issues"`) — `quote_ident`
/// alone would (correctly, but unhelpfully) quote the whole string as one identifier containing
/// a literal dot instead of `"entities"."jira_issues"`. Splits on the first `.` and quotes each
/// part separately; a name with no `.` (should not happen for a managed table, kept only as a
/// defensive fallback) is quoted as a single unqualified identifier.
pub fn quote_qualified_ident(name: &str) -> String {
    match name.split_once('.') {
        Some((schema, table)) => format!("{}.{}", quote_ident(schema), quote_ident(table)),
        None => quote_ident(name),
    }
}

pub fn quote_literal(value: &str) -> String {
    format!("'{}'", value.replace('\'', "''"))
}
