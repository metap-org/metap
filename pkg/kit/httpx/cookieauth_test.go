package httpx

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOnlySafeMethodsSkipTheCsrfCheck(t *testing.T) {
	for _, m := range []string{"GET", "HEAD", "OPTIONS"} {
		require.False(t, RequiresCSRFCheck(m), "%s must be exempt", m)
	}
	for _, m := range []string{"POST", "PUT", "PATCH", "DELETE"} {
		require.True(t, RequiresCSRFCheck(m), "%s must be CSRF-checked", m)
	}
}

func TestCsrfMatchesOnlyWhenBothArePresentAndEqual(t *testing.T) {
	require.True(t, CSRFMatches("abc", "abc", true, true), "expected match")
	require.False(t, CSRFMatches("abc", "different", true, true), "expected no match")
	require.False(t, CSRFMatches("abc", "", true, false), "header missing must not pass")
	require.False(t, CSRFMatches("", "abc", false, true), "cookie missing must not pass")
	require.False(t, CSRFMatches("", "", false, false), "both missing must not pass")
}

func TestEmptyValuesNeverMatchEachOther(t *testing.T) {
	require.False(t, CSRFMatches("", "", true, true), "both present but empty must not match")
	require.False(t, CSRFMatches("", "abc", true, false), "expected no match")
}
