//! Builds a `metap_query::ListInput` from a `{camel}List` field's arguments — the GraphQL
//! counterpart to REST's `parse_list_input` (`crates/metap-http/src/routes/records.rs`) and
//! `metap-grpc`'s `list_input_from_query`. Same defaults (limit 30, max 200).

use async_graphql::dynamic::ObjectAccessor;
use async_graphql::Error as GqlError;
use metap_query::ListInput;
use serde_json::Value;

pub fn list_input_from_args(args: &ObjectAccessor<'_>) -> Result<ListInput, GqlError> {
    let limit = match args.get("limit") {
        None => 30,
        Some(v) if v.is_null() => 30,
        Some(v) => {
            let n = v.i64()?;
            if n > 0 && n <= 200 {
                n
            } else {
                return Err(GqlError::new("`limit` must be a positive integer no greater than 200."));
            }
        }
    };

    let filters: Vec<(String, String)> = match args.get("filter") {
        Some(v) if !v.is_null() => {
            let json = v.as_value().clone().into_json()?;
            match json {
                Value::Object(map) => map.into_iter().map(|(k, v)| (k, filter_value_to_string(&v))).collect(),
                _ => Vec::new(),
            }
        }
        _ => Vec::new(),
    };

    Ok(ListInput {
        limit,
        sort: string_arg(args, "sort"),
        filters,
        cursor: string_arg(args, "cursor"),
        list_view: string_arg(args, "listView"),
        jql: string_arg(args, "jql"),
    })
}

fn string_arg(args: &ObjectAccessor<'_>, name: &str) -> Option<String> {
    args.get(name)
        .filter(|v| !v.is_null())
        .and_then(|v| v.string().ok().map(str::to_string))
}

fn filter_value_to_string(v: &Value) -> String {
    match v {
        Value::String(s) => s.clone(),
        other => other.to_string(),
    }
}
