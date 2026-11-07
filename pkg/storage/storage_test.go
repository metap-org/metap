package storage

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestValidateKeyAcceptsANormalKey(t *testing.T) {
	require.NoError(t, validateKey("avatars/user-123.png"), "unexpected error")
}

func TestValidateKeyRejectsTraversalSegments(t *testing.T) {
	for _, bad := range []string{"../other-tenant/secret", "a/../b", "./a"} {
		require.Error(t, validateKey(bad), "expected %q to be rejected", bad)
	}
}

func TestValidateKeyRejectsLeadingSlashEmptyAndControlChars(t *testing.T) {
	require.Error(t, validateKey("/absolute"), "expected rejection for leading slash")
	require.Error(t, validateKey(""), "expected rejection for empty key")
	require.Error(t, validateKey("a\x00b"), "expected rejection for control character")
	long := make([]byte, 1025)
	for i := range long {
		long[i] = 'a'
	}
	require.Error(t, validateKey(string(long)), "expected rejection for overlong key")
}

func TestScopedKeyPrefixesTheTenantAndRejectsInvalidKeys(t *testing.T) {
	tenant := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	got, err := ScopedKey(tenant, "files/a.txt")
	require.NoError(t, err)
	require.Equal(t, "11111111-1111-1111-1111-111111111111/files/a.txt", got)
	_, err = ScopedKey(tenant, "../other-tenant/secret")
	require.Error(t, err)
}
