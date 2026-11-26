package signin

import (
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/auth/identity"
	"github.com/metap-org/metap/pkg/crud"
	"github.com/metap-org/metap/pkg/kit/errcode"
)

func asServiceError(t *testing.T, err error) *crud.ServiceError {
	var se *crud.ServiceError
	require.ErrorAs(t, err, &se)
	return se
}

func TestIdentityErrorMapping(t *testing.T) {
	for _, c := range []struct {
		in     error
		status int
		code   string
	}{
		{identity.ErrInvalidCredentials, http.StatusUnauthorized, errcode.InvalidCredentials},
		{identity.ErrLocked, http.StatusTooManyRequests, errcode.AccountLocked},
		{identity.ErrDisabled, http.StatusForbidden, errcode.AccountDisabled},
		{identity.ErrWeakPassword, http.StatusBadRequest, errcode.WeakPassword},
	} {
		se := asServiceError(t, IdentityError(c.in))
		assert.EqualValues(t, c.status, se.Status, c.in)
		assert.Equal(t, c.code, se.Code, c.in)
	}
	boom := errors.New("boom")
	assert.Same(t, boom, IdentityError(boom), "anything else is internal")
	assert.Contains(t, asServiceError(t, IdentityError(identity.ErrWeakPassword)).FieldErrors, "password")
}

func TestMFAErrorMapping(t *testing.T) {
	assert.EqualValues(t, http.StatusUnauthorized, asServiceError(t, MFAError(identity.ErrMFAChallenge)).Status)
	assert.EqualValues(t, http.StatusUnauthorized, asServiceError(t, MFAError(identity.ErrMFAInvalidCode)).Status)
	assert.EqualValues(t, http.StatusTooManyRequests, asServiceError(t, MFAError(identity.ErrMFALocked)).Status)
	assert.EqualValues(t, http.StatusConflict, asServiceError(t, MFAError(identity.ErrMFAAlreadyOn)).Status)
	assert.EqualValues(t, http.StatusConflict, asServiceError(t, MFAError(identity.ErrMFANotEnrolled)).Status)
	boom := errors.New("boom")
	assert.Same(t, boom, MFAError(boom))
}

func TestInvitationErrorMapping(t *testing.T) {
	assert.Equal(t, errcode.InvitationInvalid, asServiceError(t, InvitationError(identity.ErrInvitationInvalid)).Code)
	assert.EqualValues(t, http.StatusConflict, asServiceError(t, InvitationError(identity.ErrInvitationNeedsSignIn)).Status)
	assert.Equal(t, errcode.ResetTokenInvalid, asServiceError(t, InvitationError(identity.ErrResetInvalid)).Code)
	assert.EqualValues(t, http.StatusUnauthorized, asServiceError(t, InvitationError(identity.ErrInvalidCredentials)).Status,
		"the sign-in errors fall through to IdentityError")
}

func TestPasswordMode(t *testing.T) {
	assert.Equal(t, "set", passwordMode(true, false))
	assert.Equal(t, "set", passwordMode(true, true))
	assert.Equal(t, "confirm", passwordMode(false, true))
	assert.Equal(t, "provider", passwordMode(false, false))
}

func TestTenantNameBlocked(t *testing.T) {
	assert.True(t, TenantNameBlocked("Admin", nil), "reserved")
	assert.True(t, TenantNameBlocked("  A-D_M.i n  ", nil), "folded to letters and digits")
	assert.True(t, TenantNameBlocked("Acme Corp", []string{"acmecorp"}), "the platform's own list")
	assert.False(t, TenantNameBlocked("Acme Labs", []string{"somethingelse"}))
}

func TestSameOrigin(t *testing.T) {
	assert.True(t, SameOrigin("https://app.acme.io/x", "https://app.acme.io"))
	assert.False(t, SameOrigin("https://evil.io/x", "https://app.acme.io"))
	assert.False(t, SameOrigin("//evil.io", "https://app.acme.io"))
	assert.False(t, SameOrigin("", "https://app.acme.io"))
}
