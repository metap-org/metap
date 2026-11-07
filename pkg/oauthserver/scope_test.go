package oauthserver

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestScopeSubsetAcceptsEqualOrNarrower(t *testing.T) {
	require.True(t, ScopeIsSubset("read:zones", "read:zones write:zones"), "expected subset")
	require.True(t, ScopeIsSubset("read:zones write:zones", "write:zones read:zones"), "expected subset regardless of order")
	require.True(t, ScopeIsSubset("", "read:zones"), "empty requested scope is always a subset")
	require.True(t, ScopeIsSubset("", ""), "empty/empty is a subset")
}

func TestScopeSubsetRejectsAnythingNotGranted(t *testing.T) {
	require.False(t, ScopeIsSubset("read:zones write:zones", "read:zones"), "expected not a subset")
	require.False(t, ScopeIsSubset("delete:zones", "read:zones write:zones"), "expected not a subset")
}
