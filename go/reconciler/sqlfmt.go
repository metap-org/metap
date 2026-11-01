// Tiny SQL-quoting helpers shared by executor and backfill. DDL statements do not support bind
// parameters at all (unlike a SELECT), so identifiers/literals built from metadata have to be
// quoted and inlined — safe only because they come exclusively from server-authored,
// MetadataCompiler-validated metadata (never request input).
package reconciler

import "strings"

func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// quoteQualifiedIdent: every physical table this crate manages lives in a schema-qualified name
// (e.g. "entities.jira_issues") — quoteIdent alone would (correctly, but unhelpfully) quote the
// whole string as one identifier containing a literal dot instead of "entities"."jira_issues".
func quoteQualifiedIdent(name string) string {
	schema, table, ok := strings.Cut(name, ".")
	if !ok {
		return quoteIdent(name)
	}
	return quoteIdent(schema) + "." + quoteIdent(table)
}

func quoteLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}
