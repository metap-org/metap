//! Tiny SQL-quoting helpers shared by `executor` and `backfill`. DDL statements do not support
//! bind parameters at all (unlike a `SELECT`), so identifiers/literals built from metadata have
//! to be quoted and inlined — safe only because they come exclusively from server-authored,
//! `MetadataCompiler`-validated metadata (never request input), same reasoning as
//! `crates/metap-peripherals/src/index_reconciler.rs`'s `quote_identifier`/`quote_literal`.

pub fn quote_ident(name: &str) -> String {
    format!("\"{}\"", name.replace('"', "\"\""))
}

pub fn quote_literal(value: &str) -> String {
    format!("'{}'", value.replace('\'', "''"))
}
