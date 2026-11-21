package control

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestUserPassRoundTrip(t *testing.T) {
	stored := EncodeUserPass("alice", `p"a:ss\word`)
	assert.True(t, IsReservedCredentialValue(stored))
	user, pass, ok := DecodeUserPass(stored)
	assert.True(t, ok)
	assert.Equal(t, "alice", user)
	assert.Equal(t, `p"a:ss\word`, pass)
	assert.Equal(t, "Basic YWxpY2U6cCJhOnNzXHdvcmQ=", AuthorizationFromCredential(stored))
}

func TestAuthorizationFromCredentialPassesOtherValuesThrough(t *testing.T) {
	assert.Equal(t, "Bearer abc", AuthorizationFromCredential("Bearer abc"))
	_, _, ok := DecodeUserPass("Bearer abc")
	assert.False(t, ok)
	_, _, ok = DecodeUserPass("userpass:not json")
	assert.False(t, ok)
}
