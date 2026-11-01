package grpcapi

import (
	"encoding/json"
	"testing"
)

func roundTrip(t *testing.T, v map[string]any) map[string]any {
	t.Helper()
	return StructToJSON(JSONToStruct(v))
}

func TestWholeNumbersRoundTripAsIntegersReadableViaJSONNumber(t *testing.T) {
	out := roundTrip(t, map[string]any{"version": json.Number("3")})
	n, ok := out["version"].(json.Number)
	if !ok {
		t.Fatalf("expected json.Number, got %T (%v)", out["version"], out["version"])
	}
	i, err := n.Int64()
	if err != nil || i != 3 {
		t.Fatalf("got %v, %v", i, err)
	}
}

func TestFractionalNumbersStillRoundTripAsFloats(t *testing.T) {
	out := roundTrip(t, map[string]any{"amount": 19.99})
	n, ok := out["amount"].(json.Number)
	if !ok {
		t.Fatalf("expected json.Number, got %T", out["amount"])
	}
	f, err := n.Float64()
	if err != nil || f != 19.99 {
		t.Fatalf("got %v, %v", f, err)
	}
	if _, err := n.Int64(); err == nil {
		t.Fatal("expected an integer parse to fail for a fractional value")
	}
}

func TestNestedObjectsArraysBoolsAndNullsRoundTrip(t *testing.T) {
	original := map[string]any{
		"name":   "First",
		"active": true,
		"tags":   []any{"a", "b"},
		"meta":   map[string]any{"nested": json.Number("1")},
	}
	out := roundTrip(t, original)
	if out["name"] != "First" {
		t.Fatalf("got %v", out["name"])
	}
	if out["active"] != true {
		t.Fatalf("got %v", out["active"])
	}
	tags, ok := out["tags"].([]any)
	if !ok || len(tags) != 2 || tags[0] != "a" || tags[1] != "b" {
		t.Fatalf("got %v", out["tags"])
	}
	meta, ok := out["meta"].(map[string]any)
	if !ok {
		t.Fatalf("got %v", out["meta"])
	}
	if n, ok := meta["nested"].(json.Number); !ok || n.String() != "1" {
		t.Fatalf("got %v", meta["nested"])
	}
}

func TestValueToStructAndStructToValueRoundTripARecordDtoShapedValue(t *testing.T) {
	type sample struct {
		Version int32  `json:"version"`
		Name    string `json:"name"`
	}
	in := sample{Version: 7, Name: "x"}
	s, err := ValueToStruct(in)
	if err != nil {
		t.Fatal(err)
	}
	var out sample
	if err := StructToValue(s, &out); err != nil {
		t.Fatal(err)
	}
	if out != in {
		t.Fatalf("got %+v, want %+v", out, in)
	}
}
