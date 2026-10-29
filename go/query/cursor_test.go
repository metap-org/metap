package query

import (
	"encoding/base64"
	"testing"
)

func strPtr(s string) *string { return &s }

func TestCursorRoundTrips(t *testing.T) {
	c := Cursor{Field: "createdAt", Value: strPtr("2026-08-01T00:00:00Z"), ID: "123e4567-e89b-12d3-a456-426614174000", Dir: SortDesc}
	encoded := EncodeCursor(c)
	decoded, ok := DecodeCursor(encoded)
	if !ok || decoded.Field != c.Field || decoded.ID != c.ID || decoded.Dir != c.Dir ||
		decoded.Value == nil || *decoded.Value != *c.Value {
		t.Fatalf("expected %+v, got %+v ok=%v", c, decoded, ok)
	}
}

func TestCursorRoundTripsANullSortValue(t *testing.T) {
	c := Cursor{Field: "dueDate", Value: nil, ID: "123e4567-e89b-12d3-a456-426614174000", Dir: SortAsc}
	encoded := EncodeCursor(c)
	decoded, ok := DecodeCursor(encoded)
	if !ok || decoded.Value != nil || decoded.Field != c.Field || decoded.ID != c.ID || decoded.Dir != c.Dir {
		t.Fatalf("expected %+v, got %+v ok=%v", c, decoded, ok)
	}
}

func TestCursorRejectsGarbage(t *testing.T) {
	if _, ok := DecodeCursor("not-base64!!!"); ok {
		t.Fatal("expected invalid base64 to fail")
	}
	if _, ok := DecodeCursor(base64StdEncode("{}")); ok {
		t.Fatal("expected a cursor missing required fields to fail")
	}
}

func TestCursorRejectsNonUUIDID(t *testing.T) {
	raw := base64StdEncode(`{"field":"createdAt","value":"x","id":"not-a-uuid","dir":"asc"}`)
	if _, ok := DecodeCursor(raw); ok {
		t.Fatal("expected a non-UUID id to fail")
	}
}

func base64StdEncode(s string) string {
	return base64.StdEncoding.EncodeToString([]byte(s))
}
