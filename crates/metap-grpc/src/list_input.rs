//! Builds a `metap_query::ListInput` from `ListRequest.query` — the gRPC counterpart to REST's
//! `parse_list_input` (`crates/metap-http/src/routes/records.rs`). Same defaults (limit 30,
//! max 200) and same "unknown/malformed value degrades gracefully rather than erroring" posture
//! for everything except `limit`, matching REST's behavior field-for-field so a caller can't
//! observe a difference in list semantics between the two transports.

use metap_query::ListInput;
use serde_json::{Map, Value};
use tonic::Status;

use crate::convert::{get_i64, get_str};

pub fn list_input_from_query(query: Option<Map<String, Value>>) -> Result<ListInput, Status> {
    let query = query.unwrap_or_default();

    let limit = match get_i64(&query, "limit") {
        None => 30,
        Some(n) if n > 0 && n <= 200 => n,
        Some(_) => {
            return Err(Status::invalid_argument(
                "`limit` must be a positive integer no greater than 200.",
            ))
        }
    };

    let filters: Vec<(String, String)> = match query.get("filter") {
        Some(Value::Object(map)) => map
            .iter()
            .map(|(k, v)| (k.clone(), value_to_filter_string(v)))
            .collect(),
        _ => Vec::new(),
    };

    Ok(ListInput {
        limit,
        sort: get_str(&query, "sort").map(str::to_string),
        filters,
        cursor: get_str(&query, "cursor").map(str::to_string),
        list_view: get_str(&query, "listView").map(str::to_string),
        jql: get_str(&query, "jql").map(str::to_string),
    })
}

/// `metap_query::plan_list`'s filter values are always strings (mirroring REST's querystring,
/// which is string-typed at the transport level) — a non-string filter value coming through a
/// `Struct` (numbers/bools are legal JSON there, unlike a real querystring) is coerced to its
/// string form rather than rejected, so a gRPC caller can write `{"filter": {"age": 30}}`
/// without needing to know REST's filter values are conventionally strings.
fn value_to_filter_string(v: &Value) -> String {
    match v {
        Value::String(s) => s.clone(),
        other => other.to_string(),
    }
}
