//! Mirrors `packages/core/src/core/query/cursor.ts` exactly: an opaque, base64-encoded
//! JSON cursor. Never trust its contents further than shape-checking here — `QueryPlanner`
//! separately validates it matches the query's actual resolved sort.

use base64::engine::general_purpose::STANDARD as BASE64;
use base64::Engine;
use serde::{Deserialize, Serialize};

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum SortDir {
    Asc,
    Desc,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Cursor {
    pub field: String,
    /// `None` when the page's last row had a SQL `NULL` sort value — distinct from `Some("")`,
    /// which is a real empty string. Collapsing both into the same empty-string encoding used to
    /// be exactly the keyset-pagination bug `AUDIT_2.md` flagged: a `NULL` sort value can never
    /// satisfy `sort_expr > ''`/`sort_expr = ''` under SQL's three-valued logic (`NULL` compared
    /// to anything is `unknown`, never `true`), so every row after the first `NULL` silently
    /// vanished from later pages. `QueryPlanner::plan_list` branches on this explicitly instead.
    pub value: Option<String>,
    pub id: String,
    pub dir: SortDir,
}

fn is_uuid(s: &str) -> bool {
    // Same shape check as cursor.ts's UUID_RE, not full RFC validation.
    let bytes = s.as_bytes();
    if bytes.len() != 36 {
        return false;
    }
    let hex_ranges = [(0, 8), (9, 13), (14, 18), (19, 23), (24, 36)];
    let dash_positions = [8, 13, 18, 23];
    for &pos in &dash_positions {
        if bytes[pos] != b'-' {
            return false;
        }
    }
    for (start, end) in hex_ranges {
        if !bytes[start..end].iter().all(|b| b.is_ascii_hexdigit()) {
            return false;
        }
    }
    true
}

pub fn encode_cursor(cursor: &Cursor) -> String {
    let json = serde_json::to_string(cursor).expect("Cursor always serializes");
    BASE64.encode(json.as_bytes())
}

pub fn decode_cursor(raw: &str) -> Option<Cursor> {
    let decoded = BASE64.decode(raw).ok()?;
    let text = String::from_utf8(decoded).ok()?;
    let cursor: Cursor = serde_json::from_str(&text).ok()?;
    if !is_uuid(&cursor.id) {
        return None;
    }
    Some(cursor)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn round_trips() {
        let cursor = Cursor {
            field: "createdAt".to_string(),
            value: Some("2026-08-01T00:00:00Z".to_string()),
            id: "123e4567-e89b-12d3-a456-426614174000".to_string(),
            dir: SortDir::Desc,
        };
        let encoded = encode_cursor(&cursor);
        assert_eq!(decode_cursor(&encoded), Some(cursor));
    }

    #[test]
    fn round_trips_a_null_sort_value() {
        let cursor = Cursor {
            field: "dueDate".to_string(),
            value: None,
            id: "123e4567-e89b-12d3-a456-426614174000".to_string(),
            dir: SortDir::Asc,
        };
        let encoded = encode_cursor(&cursor);
        assert_eq!(decode_cursor(&encoded), Some(cursor));
    }

    #[test]
    fn rejects_garbage() {
        assert_eq!(decode_cursor("not-base64!!!"), None);
        assert_eq!(
            decode_cursor(&base64::engine::general_purpose::STANDARD.encode("{}")),
            None
        );
    }

    #[test]
    fn rejects_non_uuid_id() {
        let raw = base64::engine::general_purpose::STANDARD
            .encode(r#"{"field":"createdAt","value":"x","id":"not-a-uuid","dir":"asc"}"#);
        assert_eq!(decode_cursor(&raw), None);
    }
}
