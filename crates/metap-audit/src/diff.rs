use std::collections::HashSet;

use serde_json::json;

use crate::entry::JsonObject;

/// Shallow, top-level-key diff between two record data blobs — `data: JsonObject` is already the
/// flat, post-validation record shape every entity works with, so nothing here needs to recurse
/// into nested objects/arrays; a changed key's *entire* new value is recorded, not a sub-diff of
/// it. No JSON-diff utility exists anywhere else in this codebase (checked before writing this) —
/// this is deliberately the smallest useful shape, not a general JSON-patch implementation.
///
/// Only keys where `before != after` are included, as `{"before": <old or null>, "after": <new or
/// null>}` — a key present in one side only reads unambiguously as added/removed via the `null`.
pub fn diff_json_objects(before: &JsonObject, after: &JsonObject) -> JsonObject {
    let mut out = JsonObject::new();
    let keys: HashSet<&String> = before.keys().chain(after.keys()).collect();
    for key in keys {
        let b = before.get(key);
        let a = after.get(key);
        if b != a {
            out.insert(key.clone(), json!({ "before": b, "after": a }));
        }
    }
    out
}

#[cfg(test)]
mod tests {
    use serde_json::json;

    use super::*;

    fn obj(pairs: &[(&str, serde_json::Value)]) -> JsonObject {
        pairs.iter().map(|(k, v)| (k.to_string(), v.clone())).collect()
    }

    #[test]
    fn no_diff_when_objects_are_equal() {
        let before = obj(&[("name", json!("Alice")), ("age", json!(30))]);
        let after = before.clone();
        assert!(diff_json_objects(&before, &after).is_empty());
    }

    #[test]
    fn reports_a_changed_key() {
        let before = obj(&[("name", json!("Alice"))]);
        let after = obj(&[("name", json!("Bob"))]);
        let diff = diff_json_objects(&before, &after);
        assert_eq!(diff.len(), 1);
        assert_eq!(diff["name"], json!({ "before": "Alice", "after": "Bob" }));
    }

    #[test]
    fn reports_an_added_key_with_before_null() {
        let before = obj(&[]);
        let after = obj(&[("name", json!("Alice"))]);
        let diff = diff_json_objects(&before, &after);
        assert_eq!(diff["name"], json!({ "before": null, "after": "Alice" }));
    }

    #[test]
    fn reports_a_removed_key_with_after_null() {
        let before = obj(&[("name", json!("Alice"))]);
        let after = obj(&[]);
        let diff = diff_json_objects(&before, &after);
        assert_eq!(diff["name"], json!({ "before": "Alice", "after": null }));
    }

    #[test]
    fn unchanged_keys_are_omitted_from_the_diff() {
        let before = obj(&[("name", json!("Alice")), ("age", json!(30))]);
        let after = obj(&[("name", json!("Alice")), ("age", json!(31))]);
        let diff = diff_json_objects(&before, &after);
        assert_eq!(diff.len(), 1);
        assert!(diff.contains_key("age"));
        assert!(!diff.contains_key("name"));
    }
}
