package grpcapi

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func roundTrip(t *testing.T, v map[string]any) map[string]any {
	t.Helper()
	return StructToJSON(JSONToStruct(v))
}

func TestWholeNumbersRoundTripAsIntegersReadableViaJSONNumber(t *testing.T) {
	out := roundTrip(t, map[string]any{"version": json.Number("3")})
	n, ok := out["version"].(json.Number)
	require.True(t, ok, "expected json.Number, got %T (%v)", out["version"], out["version"])
	i, err := n.Int64()
	require.NoError(t, err, "got %v,", i)
	require.EqualValues(t, 3, i, "got %v, %v", i, err)
}

func TestFractionalNumbersStillRoundTripAsFloats(t *testing.T) {
	out := roundTrip(t, map[string]any{"amount": 19.99})
	n, ok := out["amount"].(json.Number)
	require.True(t, ok, "expected json.Number, got %T", out["amount"])
	f, err := n.Float64()
	require.NoError(t, err, "got %v,", f)
	require.Equal(t, 19.99, f, "got %v, %v", f, err)
	_, err = n.Int64()
	require.Error(t, err, "expected an integer parse to fail for a fractional value")
}

func TestNestedObjectsArraysBoolsAndNullsRoundTrip(t *testing.T) {
	original := map[string]any{
		"name":   "First",
		"active": true,
		"tags":   []any{"a", "b"},
		"meta":   map[string]any{"nested": json.Number("1")},
	}
	out := roundTrip(t, original)
	require.Equal(t, "First", out["name"], "got %v", out["name"])
	require.Equal(t, true, out["active"], "got %v", out["active"])
	tags, ok := out["tags"].([]any)
	require.True(t, ok, "got %v", out["tags"])
	require.Len(t, tags, 2, "got %v", out["tags"])
	require.Equal(t, "a", tags[0], "got %v", out["tags"])
	require.Equal(t, "b", tags[1], "got %v", out["tags"])
	meta, ok := out["meta"].(map[string]any)
	require.True(t, ok, "got %v", out["meta"])
	n, ok := meta["nested"].(json.Number)
	require.True(t, ok, "got %v", meta["nested"])
	require.Equal(t, "1", n.String(), "got %v", meta["nested"])
}

func TestValueToStructAndStructToValueRoundTripARecordDtoShapedValue(t *testing.T) {
	type sample struct {
		Version int32  `json:"version"`
		Name    string `json:"name"`
	}
	in := sample{Version: 7, Name: "x"}
	s, err := ValueToStruct(in)
	require.NoError(t, err)
	var out sample
	require.NoError(t, StructToValue(s, &out))
	require.Equal(t, in, out, "got %+v, want %+v", out, in)
}
