// google.protobuf.Struct <-> encoding/json — the seam every RPC crosses to hand a record (or a
// query filter object) to/from crud.RecordBackend, which only ever speaks JSON. Ported from
// crates/metap-grpc/src/convert.rs. The one real lossy edge is numbers — protobuf's
// structpb.Value is always a float64 for a number, so an integer that doesn't fit exactly in a
// float64 would round-trip incorrectly; this is protobuf's own Struct design limitation, not
// something this file can fix. jsonNumberFromFloat64 reconstructs an integer-backed
// json.Number whenever the float64 exactly represents one, matching every RecordDto integer
// field (version, a Money-kind field, ...) staying readable as an integer by any caller
// downstream of this conversion, same as REST's plain encoding/json values already behave for
// the same field.
package grpcapi

import (
	"bytes"
	"encoding/json"
	"math"
	"strconv"

	"google.golang.org/protobuf/types/known/structpb"
)

// StructToJSON converts a google.protobuf.Struct into a raw JSON object (map[string]any).
func StructToJSON(s *structpb.Struct) map[string]any {
	if s == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(s.Fields))
	for k, v := range s.Fields {
		out[k] = pbValueToJSON(v)
	}
	return out
}

// JSONToStruct converts a JSON-object-shaped Go value into a google.protobuf.Struct. A
// non-object top-level value has no Struct representation — mirrors the Rust json_to_struct's
// own fallback (an empty Struct) rather than panicking, since a malformed caller-supplied value
// should surface as a normal validation error downstream in CrudService, not a panic here.
func JSONToStruct(v any) *structpb.Struct {
	m, ok := v.(map[string]any)
	if !ok {
		return &structpb.Struct{}
	}
	fields := make(map[string]*structpb.Value, len(m))
	for k, val := range m {
		fields[k] = jsonToPbValue(val)
	}
	return &structpb.Struct{Fields: fields}
}

func pbValueToJSON(v *structpb.Value) any {
	if v == nil {
		return nil
	}
	switch kind := v.GetKind().(type) {
	case *structpb.Value_NullValue, nil:
		return nil
	case *structpb.Value_NumberValue:
		return jsonNumberFromFloat64(kind.NumberValue)
	case *structpb.Value_StringValue:
		return kind.StringValue
	case *structpb.Value_BoolValue:
		return kind.BoolValue
	case *structpb.Value_StructValue:
		return StructToJSON(kind.StructValue)
	case *structpb.Value_ListValue:
		items := kind.ListValue.GetValues()
		out := make([]any, len(items))
		for i, item := range items {
			out[i] = pbValueToJSON(item)
		}
		return out
	default:
		return nil
	}
}

// jsonNumberFromFloat64 mirrors the Rust json_number_from_f64: a whole-valued float64 round-trips
// as a json.Number holding its integer textual form, so callers reading it back via
// json.Marshal/Unmarshal into an int64 field keep working exactly as REST's plain JSON body
// already does for the same field.
func jsonNumberFromFloat64(n float64) json.Number {
	if !math.IsInf(n, 0) && !math.IsNaN(n) && n == math.Trunc(n) {
		if n >= math.MinInt64 && n <= math.MaxInt64 {
			return json.Number(strconv.FormatInt(int64(n), 10))
		}
		if n >= 0 && n <= math.MaxUint64 {
			return json.Number(strconv.FormatUint(uint64(n), 10))
		}
	}
	return json.Number(strconv.FormatFloat(n, 'g', -1, 64))
}

func jsonToPbValue(v any) *structpb.Value {
	switch val := v.(type) {
	case nil:
		return structpb.NewNullValue()
	case bool:
		return structpb.NewBoolValue(val)
	case json.Number:
		f, _ := val.Float64()
		return structpb.NewNumberValue(f)
	case float64:
		return structpb.NewNumberValue(val)
	case int64:
		return structpb.NewNumberValue(float64(val))
	case int:
		return structpb.NewNumberValue(float64(val))
	case string:
		return structpb.NewStringValue(val)
	case []any:
		values := make([]*structpb.Value, len(val))
		for i, item := range val {
			values[i] = jsonToPbValue(item)
		}
		return structpb.NewListValue(&structpb.ListValue{Values: values})
	case map[string]any:
		return structpb.NewStructValue(JSONToStruct(val))
	default:
		return structpb.NewNullValue()
	}
}

// GetStr reads a string field out of a JSON object standing in for an options bag (e.g.
// ListRequest.query) — returns ("", false) for a missing key or a non-string value, same
// leniency parseListInput (REST) already has for a missing querystring param.
func GetStr(m map[string]any, key string) (string, bool) {
	v, ok := m[key]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

// GetI64 reads an integer field out of a JSON object, accepting either a json.Number or a
// float64 (both of which pbValueToJSON/decoding can produce).
func GetI64(m map[string]any, key string) (int64, bool) {
	v, ok := m[key]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case json.Number:
		i, err := n.Int64()
		if err == nil {
			return i, true
		}
		f, err := n.Float64()
		if err != nil {
			return 0, false
		}
		return int64(f), true
	case float64:
		return int64(n), true
	default:
		return 0, false
	}
}

// ValueToStruct JSON-round-trips any Go value (a RecordDto, a RecordCapabilities, an
// AggregateSpec, ...) into a google.protobuf.Struct — the generic counterpart to the Rust
// json_to_struct(serde_json::to_value(x)) idiom used throughout service.rs/client.rs.
// json.Decoder.UseNumber keeps an integer field (version, ...) as a json.Number rather than a
// plain float64, so jsonToPbValue's json.Number branch controls the one lossy conversion
// (float64 on the wire), instead of a silent extra truncation happening here first.
func ValueToStruct(v any) (*structpb.Struct, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil, err
	}
	return JSONToStruct(m), nil
}

// StructToValue is ValueToStruct's inverse: decodes a google.protobuf.Struct into out (a
// pointer to a RecordDto, RecordCapabilities, AggregateSpec, ...) via the same JSON round trip.
func StructToValue(s *structpb.Struct, out any) error {
	b, err := json.Marshal(StructToJSON(s))
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

// StructToJSONObject decodes a google.protobuf.Struct into a map[string]json.RawMessage — the
// shape crud.JsonObject is a type alias for, used for CreateRequest/UpdateRequest/
// TransitionRequest's "data" field, which CrudService validates/coerces per-field itself rather
// than this layer trying to type it.
func StructToJSONObject(s *structpb.Struct) (map[string]json.RawMessage, error) {
	b, err := json.Marshal(StructToJSON(s))
	if err != nil {
		return nil, err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return m, nil
}
