//! §5.3 — Postgres rewrites an expression when it stores it (`pg_get_indexdef` returns a
//! canonical form that rarely matches what was typed verbatim). Empirically probed against a
//! real dev Postgres (`docker exec ... psql`, 2026-08-23) before writing this:
//! `(data ->> 'field')::text` is stored back as `((data ->> 'field'::text))` — the outer
//! (no-op) `::text` cast vanishes but Postgres adds its own `::text` cast onto the *string
//! literal* `'field'`, plus extra wrapping parens. `(data ->> 'refid')::uuid` comes back as
//! `(((data ->> 'refid'::text))::uuid)`. Comparing the raw strings would false-positive on
//! every reconcile pass and never converge (`diff()` would keep re-dropping and re-creating a
//! perfectly fine index forever). Normalize both sides the same way before comparing:
//! lowercase, strip double-quotes, strip every `::type[(args)]` cast annotation (both the
//! no-op ones Postgres adds to literals and the meaningful ones on the outer expression — see
//! the doc-comment trade-off note below), strip all parentheses (their nesting differs between
//! what was typed and what Postgres echoes back), collapse whitespace.
//!
//! **Known trade-off**: stripping *all* casts means an index whose only change is the outer
//! type cast (e.g. a field's `kind` changing from `String` to `Reference`, `text`→`uuid`) is
//! not detected as needing a rebuild. Step 2 (`docs/features/04-table-per-entity.md`) has no
//! `ALTER COLUMN TYPE` op either — changing a promoted field's `kind` after it is first
//! reconciled isn't a supported operation yet, so this is consistent with (not a regression
//! from) the rest of this crate's current scope, not a hidden gap.

pub fn normalize_expr(expr: &str) -> String {
    let no_casts = strip_type_casts(expr);
    let lowercased = no_casts.to_lowercase();
    let bare: String = lowercased.chars().filter(|c| !matches!(c, '"' | '(' | ')')).collect();
    bare.split_whitespace().collect::<Vec<_>>().join(" ")
}

fn strip_type_casts(expr: &str) -> String {
    let chars: Vec<char> = expr.chars().collect();
    let mut out = String::with_capacity(expr.len());
    let mut i = 0;
    while i < chars.len() {
        if chars[i] == ':' && chars.get(i + 1) == Some(&':') {
            i += 2;
            while i < chars.len() && (chars[i].is_alphanumeric() || chars[i] == '_') {
                i += 1;
            }
            if chars.get(i) == Some(&'(') {
                let mut depth = 1;
                i += 1;
                while i < chars.len() && depth > 0 {
                    match chars[i] {
                        '(' => depth += 1,
                        ')' => depth -= 1,
                        _ => {}
                    }
                    i += 1;
                }
            }
            continue;
        }
        out.push(chars[i]);
        i += 1;
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn lowercases_strips_quotes_parens_and_collapses_whitespace() {
        assert_eq!(
            normalize_expr("(jsonb_extract_path_text(\"data\",  'departmentId'))"),
            "jsonb_extract_path_textdata, 'departmentid'"
        );
    }

    #[test]
    fn strips_a_bare_cast_with_no_paren_args() {
        assert_eq!(normalize_expr("(data ->> 'field')::text"), "data ->> 'field'");
    }

    #[test]
    fn strips_a_cast_with_paren_args_like_numeric() {
        assert_eq!(
            normalize_expr("(data ->> 'amount')::numeric(18,4)"),
            "data ->> 'amount'"
        );
    }

    #[test]
    fn matches_what_postgres_echoes_back_for_a_text_expression_index() {
        // Probed live: `(data ->> 'field')::text` is stored as `((data ->> 'field'::text))`.
        let typed = normalize_expr("(data ->> 'field')::text");
        let echoed = normalize_expr("((data ->> 'field'::text))");
        assert_eq!(typed, echoed);
    }

    #[test]
    fn matches_what_postgres_echoes_back_for_a_uuid_expression_index() {
        // Probed live: `(data ->> 'refid')::uuid` is stored as
        // `(((data ->> 'refid'::text))::uuid)`.
        let typed = normalize_expr("(data ->> 'refid')::uuid");
        let echoed = normalize_expr("(((data ->> 'refid'::text))::uuid)");
        assert_eq!(typed, echoed);
    }

    #[test]
    fn matches_what_postgres_echoes_back_for_an_fts_gin_index() {
        // Probed live: `to_tsvector('simple', (data ->> 'description'))` is stored as
        // `to_tsvector('simple'::regconfig, (data ->> 'description'::text))`.
        let typed = normalize_expr("to_tsvector('simple', (data ->> 'description'))");
        let echoed = normalize_expr("to_tsvector('simple'::regconfig, (data ->> 'description'::text))");
        assert_eq!(typed, echoed);
    }

    #[test]
    fn matches_what_postgres_echoes_back_for_a_trigram_gin_index() {
        // Probed live: `(data ->> 'title') gin_trgm_ops` is stored as
        // `((data ->> 'title'::text)) gin_trgm_ops`.
        let typed = normalize_expr("(data ->> 'title') gin_trgm_ops");
        let echoed = normalize_expr("((data ->> 'title'::text)) gin_trgm_ops");
        assert_eq!(typed, echoed);
    }
}
