package crud

import (
	"encoding/json"
	"testing"

	"github.com/metap-org/metap/pkg/metadata"
	"github.com/stretchr/testify/require"
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
	got := errs["name"]
	require.Len(t, got, 1, "got %v", got)
	require.Equal(t, "required", got[0], "got %v", got)
}

func TestPresentRequiredFieldPasses(t *testing.T) {
	e := ventity(vfield("name", metadata.KindString, true))
	_, errs := ValidatePayload(e, vobj(map[string]any{"name": "Acme"}))
	require.Nil(t, errs, "expected ok, got %v", errs)
}

func TestWrongJSONTypeIsRejected(t *testing.T) {
	e := ventity(vfield("score", metadata.KindNumber, false))
	_, errs := ValidatePayload(e, vobj(map[string]any{"score": "not-a-number"}))
	got := errs["score"]
	require.Len(t, got, 1, "got %v", got)
	require.Equal(t, "invalid_type", got[0], "got %v", got)
}

func TestEnumValueNotInEnumValuesIsRejected(t *testing.T) {
	f := vfield("status", metadata.KindEnum, false)
	f.EnumValues = []string{"draft", "active"}
	_, errs := ValidatePayload(ventity(f), vobj(map[string]any{"status": "bogus"}))
	require.NotNil(t, errs, "expected error")
}

func TestEnumValueInEnumValuesPasses(t *testing.T) {
	f := vfield("status", metadata.KindEnum, false)
	f.EnumValues = []string{"draft", "active"}
	_, errs := ValidatePayload(ventity(f), vobj(map[string]any{"status": "active"}))
	require.Nil(t, errs, "expected ok, got %v", errs)
}

func TestUnknownExtraFieldsPassThroughUnvalidated(t *testing.T) {
	e := ventity(vfield("name", metadata.KindString, true))
	result, errs := ValidatePayload(e, vobj(map[string]any{"name": "Acme", "mystery": 123}))
	require.Nil(t, errs, "expected ok, got %v", errs)
	var mystery int
	err := json.Unmarshal(result["mystery"], &mystery)
	require.NoError(t, err, "got %v", result["mystery"])
	require.Equal(t, 123, mystery, "got %v", result["mystery"])
}

func TestOptionalFieldAbsentIsFine(t *testing.T) {
	e := ventity(vfield("phone", metadata.KindString, false))
	_, errs := ValidatePayload(e, JsonObject{})
	require.Nil(t, errs, "expected ok, got %v", errs)
}

func TestReferenceFieldWithANonUUIDStringIsRejected(t *testing.T) {
	e := ventity(vfield("parentId", metadata.KindReference, false))
	_, errs := ValidatePayload(e, vobj(map[string]any{"parentId": "not-a-uuid"}))
	got := errs["parentId"]
	require.Len(t, got, 1, "got %v", got)
	require.Equal(t, "invalid_type", got[0], "got %v", got)
}

func TestReferenceFieldValueIsCanonicalizedToLowercase(t *testing.T) {
	e := ventity(vfield("parentId", metadata.KindReference, false))
	result, errs := ValidatePayload(e, vobj(map[string]any{"parentId": "550E8400-E29B-41D4-A716-446655440000"}))
	require.Nil(t, errs, "expected ok, got %v", errs)
	var s string
	_ = json.Unmarshal(result["parentId"], &s)
	require.Equal(t, "550e8400-e29b-41d4-a716-446655440000", s, "got %q", s)
}

func TestNumberBelowMinIsRejected(t *testing.T) {
	f := vfield("qty", metadata.KindNumber, false)
	min := 1.0
	f.Min = &min
	_, errs := ValidatePayload(ventity(f), vobj(map[string]any{"qty": 0}))
	got := errs["qty"]
	require.Len(t, got, 1, "got %v", got)
	require.Equal(t, "min", got[0], "got %v", got)
}

func TestNumberAboveMaxIsRejected(t *testing.T) {
	f := vfield("qty", metadata.KindNumber, false)
	max := 10.0
	f.Max = &max
	_, errs := ValidatePayload(ventity(f), vobj(map[string]any{"qty": 11}))
	got := errs["qty"]
	require.Len(t, got, 1, "got %v", got)
	require.Equal(t, "max", got[0], "got %v", got)
}

func TestNumberWithinMinMaxPasses(t *testing.T) {
	f := vfield("qty", metadata.KindNumber, false)
	min, max := 1.0, 10.0
	f.Min, f.Max = &min, &max
	_, errs := ValidatePayload(ventity(f), vobj(map[string]any{"qty": 5}))
	require.Nil(t, errs, "expected ok, got %v", errs)
}

func TestStringShorterThanMinLengthIsRejected(t *testing.T) {
	f := vfield("code", metadata.KindString, false)
	minLen := uint32(3)
	f.MinLength = &minLen
	_, errs := ValidatePayload(ventity(f), vobj(map[string]any{"code": "ab"}))
	got := errs["code"]
	require.Len(t, got, 1, "got %v", got)
	require.Equal(t, "min_length", got[0], "got %v", got)
}

func TestStringLongerThanMaxLengthIsRejected(t *testing.T) {
	f := vfield("code", metadata.KindString, false)
	maxLen := uint32(3)
	f.MaxLength = &maxLen
	_, errs := ValidatePayload(ventity(f), vobj(map[string]any{"code": "abcd"}))
	got := errs["code"]
	require.Len(t, got, 1, "got %v", got)
	require.Equal(t, "max_length", got[0], "got %v", got)
}

func TestStringWithinLengthBoundsPasses(t *testing.T) {
	f := vfield("code", metadata.KindString, false)
	minLen, maxLen := uint32(2), uint32(4)
	f.MinLength, f.MaxLength = &minLen, &maxLen
	_, errs := ValidatePayload(ventity(f), vobj(map[string]any{"code": "abc"}))
	require.Nil(t, errs, "expected ok, got %v", errs)
}
