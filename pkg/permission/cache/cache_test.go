package cache

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateKeyAcceptsANormalKey(t *testing.T) {
	require.NoError(t, validateKey("policies:crm.customers"), "unexpected error")
}

func TestValidateKeyRejectsEmptyControlCharsAndOverlong(t *testing.T) {
	require.Error(t, validateKey(""), "expected error for empty key")
	require.Error(t, validateKey("a\x00b"), "expected error for control character")
	long := make([]byte, 257)
	for i := range long {
		long[i] = 'a'
	}
	require.Error(t, validateKey(string(long)), "expected error for overlong key")
}
