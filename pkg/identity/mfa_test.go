package identity

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// RFC 6238 Appendix B vector (SHA-1, secret "12345678901234567890", T=59s → 94287082; 6 digits: 287082).
func TestTOTPMatchesRFC6238(t *testing.T) {
	secret := []byte("12345678901234567890")
	assert.Equal(t, "287082", totpCode(secret, 59/30))
	assert.Equal(t, "081804", totpCode(secret, 1111111109/30))
}

func TestMatchTOTPWindowAndReplay(t *testing.T) {
	secret := []byte("12345678901234567890")
	now := time.Unix(1_700_000_000, 0)
	step := now.Unix() / 30
	cur := totpCode(secret, step)
	assert.Equal(t, step, matchTOTP(secret, cur, now, 0))
	assert.Equal(t, step-1, matchTOTP(secret, totpCode(secret, step-1), now, 0), "one step of drift is allowed")
	assert.Zero(t, matchTOTP(secret, totpCode(secret, step-2), now, 0), "two steps is too old")
	assert.Zero(t, matchTOTP(secret, cur, now, step), "a code already used is refused")
	assert.Zero(t, matchTOTP(secret, "12345", now, 0))
	assert.Zero(t, matchTOTP(secret, "abcdef", now, 0))
	_, err := decodeSecret("not base32!")
	require.Error(t, err)
}

func TestRecoveryCodeShapeAndHashNormalisation(t *testing.T) {
	c, err := newRecoveryCode()
	require.NoError(t, err)
	assert.Len(t, c, 11)
	assert.Equal(t, hashRecoveryCode(c), hashRecoveryCode(" "+c+" "))
}
