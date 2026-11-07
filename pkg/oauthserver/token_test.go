package oauthserver

import (
	"testing"
	"unicode"

	"github.com/stretchr/testify/require"
)

func TestGeneratedTokensAreUniqueAndURLSafe(t *testing.T) {
	a, err := GenerateOpaqueToken()
	require.NoError(t, err)
	b, err := GenerateOpaqueToken()
	require.NoError(t, err)
	require.NotEqual(t, b, a, "expected two distinct tokens")
	for _, c := range a {
		require.True(t, (unicode.IsLetter(c) || unicode.IsDigit(c) || c == '-' || c == '_'), "token %q contains a non-URL-safe character %q", a, c)
	}
	// 32 raw bytes, base64url no-pad -> 43 chars.
	require.Equal(t, 43, len(a), "expected length 43, got %d", len(a))
}

func TestHashIsDeterministicAndHex(t *testing.T) {
	raw := "a-fixed-test-token"
	h1 := HashToken(raw)
	h2 := HashToken(raw)
	require.Equal(t, h2, h1, "expected deterministic hash")
	require.Equal(t, 64, len(h1), "expected length 64, got %d", len(h1))
	for _, c := range h1 {
		require.True(t, ((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')), "hash %q contains a non-hex character %q", h1, c)
	}
	require.NotEqual(t, h1, HashToken("other"), "expected different inputs to hash differently")
}
