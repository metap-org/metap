package crud

import (
	"encoding/json"
	"testing"

	"github.com/metap-org/metap-go/metadata"
)

func vfield(name string, kind metadata.FieldKind, required bool) metadata.EntityField {
	f := metadata.EntityField{Name: name, Label: name, Kind: kind}
	if required {
		t := true
		f.Required = &t
	}
	return f
}

func ventity(fields ...metadata.EntityField) metadata.EntityDefinition {
	return metadata.EntityDefinition{Name: "test.widgets", Label: "Widget", TableName: "entities.test_widgets", Fields: fields}
}

func vraw(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func vobj(pairs map[string]any) JsonObject {
	out := JsonObject{}
	for k, v := range pairs {
		out[k] = vraw(v)
	}
	return out
}

func TestMissingRequiredFieldIsAnError(t *testing.T) {
	e := ventity(vfield("name", metadata.KindString, true))
	_, errs := ValidatePayload(e, JsonObject{})
	if got := errs["name"]; len(got) != 1 || got[0] != "required" {
		t.Fatalf("got %v", got)
	}
}

func TestPresentRequiredFieldPasses(t *testing.T) {
	e := ventity(vfield("name", metadata.KindString, true))
	_, errs := ValidatePayload(e, vobj(map[string]any{"name": "Acme"}))
	if errs != nil {
		t.Fatalf("expected ok, got %v", errs)
	}
}

func TestWrongJSONTypeIsRejected(t *testing.T) {
	e := ventity(vfield("score", metadata.KindNumber, false))
	_, errs := ValidatePayload(e, vobj(map[string]any{"score": "not-a-number"}))
	if got := errs["score"]; len(got) != 1 || got[0] != "invalid_type" {
		t.Fatalf("got %v", got)
	}
}

func TestEnumValueNotInEnumValuesIsRejected(t *testing.T) {
	f := vfield("status", metadata.KindEnum, false)
	f.EnumValues = []string{"draft", "active"}
	_, errs := ValidatePayload(ventity(f), vobj(map[string]any{"status": "bogus"}))
	if errs == nil {
		t.Fatal("expected error")
	}
}

func TestEnumValueInEnumValuesPasses(t *testing.T) {
	f := vfield("status", metadata.KindEnum, false)
	f.EnumValues = []string{"draft", "active"}
	_, errs := ValidatePayload(ventity(f), vobj(map[string]any{"status": "active"}))
	if errs != nil {
		t.Fatalf("expected ok, got %v", errs)
	}
}

func TestUnknownExtraFieldsPassThroughUnvalidated(t *testing.T) {
	e := ventity(vfield("name", metadata.KindString, true))
	result, errs := ValidatePayload(e, vobj(map[string]any{"name": "Acme", "mystery": 123}))
	if errs != nil {
		t.Fatalf("expected ok, got %v", errs)
	}
	var mystery int
	if err := json.Unmarshal(result["mystery"], &mystery); err != nil || mystery != 123 {
		t.Fatalf("got %v", result["mystery"])
	}
}

func TestOptionalFieldAbsentIsFine(t *testing.T) {
	e := ventity(vfield("phone", metadata.KindString, false))
	_, errs := ValidatePayload(e, JsonObject{})
	if errs != nil {
		t.Fatalf("expected ok, got %v", errs)
	}
}

func TestReferenceFieldWithANonUUIDStringIsRejected(t *testing.T) {
	e := ventity(vfield("parentId", metadata.KindReference, false))
	_, errs := ValidatePayload(e, vobj(map[string]any{"parentId": "not-a-uuid"}))
	if got := errs["parentId"]; len(got) != 1 || got[0] != "invalid_type" {
		t.Fatalf("got %v", got)
	}
}

func TestReferenceFieldValueIsCanonicalizedToLowercase(t *testing.T) {
	e := ventity(vfield("parentId", metadata.KindReference, false))
	result, errs := ValidatePayload(e, vobj(map[string]any{"parentId": "550E8400-E29B-41D4-A716-446655440000"}))
	if errs != nil {
		t.Fatalf("expected ok, got %v", errs)
	}
	var s string
	_ = json.Unmarshal(result["parentId"], &s)
	if s != "550e8400-e29b-41d4-a716-446655440000" {
		t.Fatalf("got %q", s)
	}
}

func TestNumberBelowMinIsRejected(t *testing.T) {
	f := vfield("qty", metadata.KindNumber, false)
	min := 1.0
	f.Min = &min
	_, errs := ValidatePayload(ventity(f), vobj(map[string]any{"qty": 0}))
	if got := errs["qty"]; len(got) != 1 || got[0] != "min" {
		t.Fatalf("got %v", got)
	}
}

func TestNumberAboveMaxIsRejected(t *testing.T) {
	f := vfield("qty", metadata.KindNumber, false)
	max := 10.0
	f.Max = &max
	_, errs := ValidatePayload(ventity(f), vobj(map[string]any{"qty": 11}))
	if got := errs["qty"]; len(got) != 1 || got[0] != "max" {
		t.Fatalf("got %v", got)
	}
}

func TestNumberWithinMinMaxPasses(t *testing.T) {
	f := vfield("qty", metadata.KindNumber, false)
	min, max := 1.0, 10.0
	f.Min, f.Max = &min, &max
	_, errs := ValidatePayload(ventity(f), vobj(map[string]any{"qty": 5}))
	if errs != nil {
		t.Fatalf("expected ok, got %v", errs)
	}
}

func TestStringShorterThanMinLengthIsRejected(t *testing.T) {
	f := vfield("code", metadata.KindString, false)
	minLen := uint32(3)
	f.MinLength = &minLen
	_, errs := ValidatePayload(ventity(f), vobj(map[string]any{"code": "ab"}))
	if got := errs["code"]; len(got) != 1 || got[0] != "min_length" {
		t.Fatalf("got %v", got)
	}
}

func TestStringLongerThanMaxLengthIsRejected(t *testing.T) {
	f := vfield("code", metadata.KindString, false)
	maxLen := uint32(3)
	f.MaxLength = &maxLen
	_, errs := ValidatePayload(ventity(f), vobj(map[string]any{"code": "abcd"}))
	if got := errs["code"]; len(got) != 1 || got[0] != "max_length" {
		t.Fatalf("got %v", got)
	}
}

func TestStringWithinLengthBoundsPasses(t *testing.T) {
	f := vfield("code", metadata.KindString, false)
	minLen, maxLen := uint32(2), uint32(4)
	f.MinLength, f.MaxLength = &minLen, &maxLen
	_, errs := ValidatePayload(ventity(f), vobj(map[string]any{"code": "abc"}))
	if errs != nil {
		t.Fatalf("expected ok, got %v", errs)
	}
}
