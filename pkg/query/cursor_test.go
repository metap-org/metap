package query

import (
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/require"
)

func strPtr(s string) *string { return &s }

func TestCursorRoundTrips(t *testing.T) {
	c := Cursor{Field: "createdAt", Value: strPtr("2026-08-01T00:00:00Z"), ID: "123e4567-e89b-12d3-a456-426614174000", Dir: SortDesc}
	encoded := EncodeCursor(c)
	decoded, ok := DecodeCursor(encoded)
	require.True(t, ok, "expected %+v, got %+v ok=%v", c, decoded, ok)
	require.Equal(t, c.Field, decoded.Field, "expected %+v, got %+v ok=%v", c, decoded, ok)
	require.Equal(t, c.ID, decoded.ID, "expected %+v, got %+v ok=%v", c, decoded, ok)
	require.Equal(t, c.Dir, decoded.Dir, "expected %+v, got %+v ok=%v", c, decoded, ok)
	require.NotNil(t, decoded.Value, "expected %+v, got %+v ok=%v", c, decoded, ok)
	require.Equal(t, *c.Value, *decoded.Value, "expected %+v, got %+v ok=%v", c, decoded, ok)
}

func TestCursorRoundTripsANullSortValue(t *testing.T) {
	c := Cursor{Field: "dueDate", Value: nil, ID: "123e4567-e89b-12d3-a456-426614174000", Dir: SortAsc}
	encoded := EncodeCursor(c)
	decoded, ok := DecodeCursor(encoded)
	require.True(t, ok, "expected %+v, got %+v ok=%v", c, decoded, ok)
	require.Nil(t, decoded.Value, "expected %+v, got %+v ok=%v", c, decoded, ok)
	require.Equal(t, c.Field, decoded.Field, "expected %+v, got %+v ok=%v", c, decoded, ok)
	require.Equal(t, c.ID, decoded.ID, "expected %+v, got %+v ok=%v", c, decoded, ok)
	require.Equal(t, c.Dir, decoded.Dir, "expected %+v, got %+v ok=%v", c, decoded, ok)
}

func TestCursorRejectsGarbage(t *testing.T) {
	_, ok := DecodeCursor("not-base64!!!")
	require.False(t, ok, "expected invalid base64 to fail")
	_, ok = DecodeCursor(base64StdEncode("{}"))
	require.False(t, ok, "expected a cursor missing required fields to fail")
}

func TestCursorRejectsNonUUIDID(t *testing.T) {
	raw := base64StdEncode(`{"field":"createdAt","value":"x","id":"not-a-uuid","dir":"asc"}`)
	_, ok := DecodeCursor(raw)
	require.False(t, ok, "expected a non-UUID id to fail")
}

func base64StdEncode(s string) string {
	return base64.StdEncoding.EncodeToString([]byte(s))
}
