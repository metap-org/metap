//! `google.protobuf.Struct` (`prost_types::Struct`) <-> `serde_json::Value` — the seam every RPC
//! crosses to hand a record (or a query filter object) to/from `CrudService`, which only ever
//! speaks `serde_json`. Deliberately hand-written rather than pulled in from a crate like
//! `pbjson-types` (~20 lines either direction, not worth an extra dependency for): protobuf's
//! `Struct`/`Value`/`ListValue` are already exactly JSON's object/scalar/array shape, so this is
//! a structural mapping, not a real conversion. The one real lossy edge is numbers — protobuf's
//! `Value::NumberValue` is always an `f64`, so an integer that doesn't fit exactly in an `f64`
//! would round-trip incorrectly; this is protobuf's own `Struct` design limitation (the same one
//! any JSON-over-protobuf bridge has), not something this module can fix, and it's an acceptable
//! tradeoff for a generic passthrough layer whose actual field typing is validated/coerced by
//! `CrudService::create`/`update` against `EntityField`/`FieldKind` right after this layer hands
//! the data off — same as REST's plain JSON body already implicitly relies on `serde_json`'s own
//! `f64`-for-non-integer-safe-numbers behavior at the edges.

use prost_types::value::Kind;
use prost_types::{ListValue, Struct, Value as PbValue};
use serde_json::{Map, Value as JsonValue};

pub fn struct_to_json(s: Struct) -> JsonValue {
    JsonValue::Object(s.fields.into_iter().map(|(k, v)| (k, pb_value_to_json(v))).collect())
}

pub fn json_to_struct(v: JsonValue) -> Struct {
    match v {
        JsonValue::Object(map) => Struct {
            fields: map.into_iter().map(|(k, v)| (k, json_to_pb_value(v))).collect(),
        },
        // A non-object top-level value has no `Struct` representation — callers of this
        // function only ever pass an already-object `JsonValue` (a `RecordDto`/`JsonObject`
        // serializes to one); treat anything else as an empty object rather than panicking,
        // since a malformed caller-supplied value should surface as a normal validation error
        // downstream in `CrudService`, not a panic here.
        _ => Struct::default(),
    }
}

fn pb_value_to_json(v: PbValue) -> JsonValue {
    match v.kind {
        None | Some(Kind::NullValue(_)) => JsonValue::Null,
        Some(Kind::NumberValue(n)) => json_number_from_f64(n),
        Some(Kind::StringValue(s)) => JsonValue::String(s),
        Some(Kind::BoolValue(b)) => JsonValue::Bool(b),
        Some(Kind::StructValue(s)) => struct_to_json(s),
        Some(Kind::ListValue(list)) => JsonValue::Array(list.values.into_iter().map(pb_value_to_json).collect()),
    }
}

/// `serde_json::Number::from_f64` always produces a float-backed `Number` — its `as_i64`/
/// `as_u64` then return `None` even for a whole number like `2.0`, since those check the
/// number's *internal representation tag*, not merely whether its value happens to be integral.
/// Every integer field a `RecordDto` carries (`version`, a `Money`-kind field, ...) would
/// otherwise silently stop being readable as an integer by any caller downstream of this
/// conversion — reconstruct an integer-backed `Number` whenever the `f64` exactly represents
/// one, matching how REST's plain `serde_json` values already behave for the same field.
fn json_number_from_f64(n: f64) -> JsonValue {
    if n.is_finite() && n.fract() == 0.0 {
        if (i64::MIN as f64..=i64::MAX as f64).contains(&n) {
            return JsonValue::Number((n as i64).into());
        }
        if (0.0..=u64::MAX as f64).contains(&n) {
            return JsonValue::Number((n as u64).into());
        }
    }
    serde_json::Number::from_f64(n)
        .map(JsonValue::Number)
        .unwrap_or(JsonValue::Null)
}

fn json_to_pb_value(v: JsonValue) -> PbValue {
    let kind = match v {
        JsonValue::Null => Kind::NullValue(0),
        JsonValue::Bool(b) => Kind::BoolValue(b),
        // `as_f64` is always `Some` for a `serde_json::Number` unless the `arbitrary_precision`
        // feature is enabled (not the case in this workspace) — see this module's doc comment
        // for the precision tradeoff this implies for very large integers.
        JsonValue::Number(n) => Kind::NumberValue(n.as_f64().unwrap_or(0.0)),
        JsonValue::String(s) => Kind::StringValue(s),
        JsonValue::Array(items) => Kind::ListValue(ListValue {
            values: items.into_iter().map(json_to_pb_value).collect(),
        }),
        JsonValue::Object(_) => Kind::StructValue(json_to_struct(v)),
    };
    PbValue { kind: Some(kind) }
}

/// Reads a string field out of a `Struct` that's standing in for an options bag (e.g.
/// `ListRequest.query`) — returns `None` for a missing key or a non-string value, same
/// leniency `parse_list_input` (REST) already has for a missing querystring param.
pub fn get_str<'a>(map: &'a Map<String, JsonValue>, key: &str) -> Option<&'a str> {
    map.get(key).and_then(JsonValue::as_str)
}

pub fn get_i64(map: &Map<String, JsonValue>, key: &str) -> Option<i64> {
    map.get(key).and_then(JsonValue::as_i64)
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn whole_numbers_round_trip_as_integers_readable_via_as_i64() {
        let round_tripped = struct_to_json(json_to_struct(json!({ "version": 3 })));
        assert_eq!(round_tripped["version"].as_i64(), Some(3));
    }

    #[test]
    fn fractional_numbers_still_round_trip_as_floats() {
        let round_tripped = struct_to_json(json_to_struct(json!({ "amount": 19.99 })));
        assert_eq!(round_tripped["amount"].as_f64(), Some(19.99));
        assert_eq!(round_tripped["amount"].as_i64(), None);
    }

    #[test]
    fn nested_objects_arrays_bools_and_nulls_round_trip() {
        let original = json!({
            "name": "First",
            "active": true,
            "tags": ["a", "b"],
            "meta": { "nested": 1 },
            "deleted_at": null,
        });
        let round_tripped = struct_to_json(json_to_struct(original.clone()));
        assert_eq!(round_tripped, original);
    }
}
