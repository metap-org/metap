//! Shared string-template parser/renderer for `EntityField.computed` (`entity.rs`'s
//! `ComputedSpec`) — one implementation used by both `compiler::validate` (which only needs the
//! token names, to check `dependsOn` coverage) and `metap_crud`'s `recompute_fields` (which
//! renders the template against a real record). Splitting the parser out here, rather than
//! duplicating "find `{...}` tokens" in each crate, is what keeps validation and execution from
//! ever disagreeing about what counts as a token — see `docs/features/13-computed-derived-field.md`.

/// Extracts every `{fieldName}` token from `expression`, in order of appearance, duplicates
/// included. An unterminated `{` (no matching `}` before the string ends) is not a token.
pub fn expression_tokens(expression: &str) -> Vec<&str> {
    let mut tokens = Vec::new();
    let bytes = expression.as_bytes();
    let mut i = 0;
    while i < bytes.len() {
        if bytes[i] == b'{' {
            if let Some(rel_end) = expression[i + 1..].find('}') {
                let end = i + 1 + rel_end;
                tokens.push(&expression[i + 1..end]);
                i = end + 1;
                continue;
            }
        }
        i += 1;
    }
    tokens
}

/// Renders `expression` by replacing every `{fieldName}` token with `resolve(fieldName)` — empty
/// string if `resolve` returns `None` for that name. An unterminated `{` is kept literal in the
/// output, matching `expression_tokens`'s definition of what counts as a real token.
pub fn render_expression(expression: &str, resolve: impl Fn(&str) -> Option<String>) -> String {
    let mut result = String::with_capacity(expression.len());
    let bytes = expression.as_bytes();
    let mut i = 0;
    while i < bytes.len() {
        if bytes[i] == b'{' {
            if let Some(rel_end) = expression[i + 1..].find('}') {
                let end = i + 1 + rel_end;
                let name = &expression[i + 1..end];
                result.push_str(&resolve(name).unwrap_or_default());
                i = end + 1;
                continue;
            }
        }
        // Push whichever char starts at byte offset `i` (not necessarily 1 byte — expression is
        // valid UTF-8 since it came in as a `String`).
        let ch = expression[i..]
            .chars()
            .next()
            .expect("i < bytes.len() so a char exists");
        result.push(ch);
        i += ch.len_utf8();
    }
    result
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn extracts_tokens_in_order_with_duplicates() {
        assert_eq!(expression_tokens("{a} and {b} and {a}"), vec!["a", "b", "a"]);
    }

    #[test]
    fn no_tokens_in_plain_text() {
        assert!(expression_tokens("plain text").is_empty());
    }

    #[test]
    fn unterminated_brace_is_not_a_token() {
        assert!(expression_tokens("plain {oops text").is_empty());
    }

    #[test]
    fn renders_known_and_missing_tokens() {
        let out = render_expression("{firstName} {lastName}", |name| match name {
            "firstName" => Some("Ada".to_string()),
            _ => None,
        });
        assert_eq!(out, "Ada ");
    }

    #[test]
    fn unterminated_brace_is_kept_literal_when_rendering() {
        let out = render_expression("hi {there", |_| Some("x".to_string()));
        assert_eq!(out, "hi {there");
    }
}
