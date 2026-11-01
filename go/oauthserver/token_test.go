package oauthserver

import (
	"testing"
	"unicode"
)

func TestGeneratedTokensAreUniqueAndURLSafe(t *testing.T) {
	a, err := GenerateOpaqueToken()
	if err != nil {
		t.Fatal(err)
	}
	b, err := GenerateOpaqueToken()
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("expected two distinct tokens")
	}
	for _, c := range a {
		if !(unicode.IsLetter(c) || unicode.IsDigit(c) || c == '-' || c == '_') {
			t.Fatalf("token %q contains a non-URL-safe character %q", a, c)
		}
	}
	// 32 raw bytes, base64url no-pad -> 43 chars.
	if len(a) != 43 {
		t.Fatalf("expected length 43, got %d", len(a))
	}
}

func TestHashIsDeterministicAndHex(t *testing.T) {
	raw := "a-fixed-test-token"
	h1 := HashToken(raw)
	h2 := HashToken(raw)
	if h1 != h2 {
		t.Fatal("expected deterministic hash")
	}
	if len(h1) != 64 {
		t.Fatalf("expected length 64, got %d", len(h1))
	}
	for _, c := range h1 {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Fatalf("hash %q contains a non-hex character %q", h1, c)
		}
	}
	if HashToken("other") == h1 {
		t.Fatal("expected different inputs to hash differently")
	}
}
