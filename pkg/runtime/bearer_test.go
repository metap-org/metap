package runtime

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseBearerExtractsToken(t *testing.T) {
	token, ok := ParseBearer("Bearer abc123")
	require.True(t, ok, "got (%q, %v)", token, ok)
	require.Equal(t, "abc123", token, "got (%q, %v)", token, ok)
}

func TestParseBearerRejectsMissingPrefix(t *testing.T) {
	_, ok := ParseBearer("Basic abc123")
	require.False(t, ok, "expected not ok")
}

func TestParseBearerRejectsEmptyToken(t *testing.T) {
	_, ok := ParseBearer("Bearer ")
	require.False(t, ok, "expected not ok for \"Bearer \"")
	_, ok = ParseBearer("Bearer")
	require.False(t, ok, "expected not ok for \"Bearer\"")
}

func TestParseBearerTrimsIncidentalWhitespace(t *testing.T) {
	token, ok := ParseBearer("Bearer  abc123  ")
	require.True(t, ok, "got (%q, %v)", token, ok)
	require.Equal(t, "abc123", token, "got (%q, %v)", token, ok)
}
