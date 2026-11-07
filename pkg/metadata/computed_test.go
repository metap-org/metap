package metadata

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExtractsTokensInOrderWithDuplicates(t *testing.T) {
	got := ExpressionTokens("{a} and {b} and {a}")
	want := []string{"a", "b", "a"}
	require.Equal(t, want, got, "got %v, want %v", got, want)
}

func TestNoTokensInPlainText(t *testing.T) {
	require.Len(t, ExpressionTokens("plain text"), 0, "expected no tokens")
}

func TestUnterminatedBraceIsNotAToken(t *testing.T) {
	require.Len(t, ExpressionTokens("plain {oops text"), 0, "expected no tokens")
}

func TestRendersKnownAndMissingTokens(t *testing.T) {
	out := RenderExpression("{firstName} {lastName}", func(name string) (string, bool) {
		if name == "firstName" {
			return "Ada", true
		}
		return "", false
	})
	require.Equal(t, "Ada ", out, "got %q", out)
}

func TestUnterminatedBraceIsKeptLiteralWhenRendering(t *testing.T) {
	out := RenderExpression("hi {there", func(string) (string, bool) { return "x", true })
	require.Equal(t, "hi {there", out, "got %q", out)
}
