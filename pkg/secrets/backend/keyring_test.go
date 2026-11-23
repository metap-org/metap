package backend

import (
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testKey(t *testing.T) string {
	t.Helper()
	k, err := GenerateKey()
	require.NoError(t, err)
	return k
}

func TestKeyringSealsAndOpens(t *testing.T) {
	ring, err := ParseKeyring("k1:" + testKey(t))
	require.NoError(t, err)
	ct, nonce, kid, err := ring.seal("METAP_TENANT_A_X", []byte("Bearer s3cret"))
	require.NoError(t, err)
	assert.Equal(t, "k1", kid)
	assert.NotContains(t, string(ct), "s3cret", "the plaintext is not in the ciphertext")

	got, err := ring.open("METAP_TENANT_A_X", kid, nonce, ct)
	require.NoError(t, err)
	assert.Equal(t, "Bearer s3cret", string(got))

	// Bound to its reference: the same row under another tenant's reference does not open.
	_, err = ring.open("METAP_TENANT_B_X", kid, nonce, ct)
	assert.Error(t, err)
	// A tampered ciphertext does not open.
	ct[0] ^= 0xff
	_, err = ring.open("METAP_TENANT_A_X", kid, nonce, ct)
	assert.Error(t, err)
}

func TestKeyringRotation(t *testing.T) {
	oldKey, newKey := testKey(t), testKey(t)
	old, err := ParseKeyring("k1:" + oldKey)
	require.NoError(t, err)
	ct, nonce, kid, err := old.seal("ref", []byte("v"))
	require.NoError(t, err)

	// The new key is first (active); the old one still opens what it sealed.
	rotated, err := ParseKeyring("k2:" + newKey + ",k1:" + oldKey)
	require.NoError(t, err)
	got, err := rotated.open("ref", kid, nonce, ct)
	require.NoError(t, err)
	assert.Equal(t, "v", string(got))
	_, _, newKid, err := rotated.seal("ref", []byte("v"))
	require.NoError(t, err)
	assert.Equal(t, "k2", newKid)

	// Dropping the old key too early is reported, not silently wrong.
	onlyNew, err := ParseKeyring("k2:" + newKey)
	require.NoError(t, err)
	_, err = onlyNew.open("ref", kid, nonce, ct)
	assert.ErrorContains(t, err, `no encryption key "k1"`)
}

func TestParseKeyringRejectsBadInput(t *testing.T) {
	for name, spec := range map[string]string{
		"empty":       "",
		"no id":       testKey(t),
		"not base64":  "k1:!!!",
		"short key":   "k1:" + base64.StdEncoding.EncodeToString([]byte("short")),
		"repeated id": "k1:" + testKey(t) + ",k1:" + testKey(t),
	} {
		_, err := ParseKeyring(spec)
		assert.Error(t, err, name)
	}
}
