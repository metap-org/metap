//go:build integration

package admin

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/internal/admin/mfa"
	"github.com/metap-org/metap/pkg/auth/identity"
)

func TestAccountMFALifecycleAndAdminReset(t *testing.T) {
	h := newIdentityHarness(t)
	tenant := uuid.New()
	admin := h.member(tenant, "admin@acme.io", "admin")
	bob := h.member(tenant, "bob@acme.io")
	asBob, asAdmin := h.as(tenant, bob.ID), h.as(tenant, admin.ID)

	st, err := h.client.Mfa.Status(asBob)
	require.NoError(t, err)
	assert.False(t, st.Enabled)

	// Confirming before starting, or with a wrong code, fails.
	_, err = h.client.Mfa.Confirm(asBob, &mfa.MfaCodeRequest{Code: "123456"})
	assert.Error(t, err)
	en, err := h.client.Mfa.Begin(asBob)
	require.NoError(t, err)
	assert.Contains(t, en.OtpauthUri, "otpauth://totp/")
	assert.Equal(t, "mfa_invalid_code", errCode(t, func() error {
		_, err := h.client.Mfa.Confirm(asBob, &mfa.MfaCodeRequest{Code: "000000"})
		return err
	}))
	code, err := identity.TOTPAt(en.Secret, time.Now())
	require.NoError(t, err)
	rec, err := h.client.Mfa.Confirm(asBob, &mfa.MfaCodeRequest{Code: code})
	require.NoError(t, err)
	require.Len(t, rec.Codes, 10)
	st, _ = h.client.Mfa.Status(asBob)
	assert.True(t, st.Enabled)
	assert.EqualValues(t, 10, st.RecoveryCodesLeft)

	// Starting over while on is refused; turning off needs a valid code.
	assert.Equal(t, "mfa_already_on", errCode(t, func() error {
		_, err := h.client.Mfa.Begin(asBob)
		return err
	}))
	err = h.client.Mfa.Disable(asBob, &mfa.MfaCodeRequest{Code: "000000"})
	assert.Error(t, err)

	// A recovery code proves it too, and is used up; new codes replace the old ones.
	fresh, err := h.client.Mfa.RegenerateRecoveryCodes(asBob, &mfa.MfaCodeRequest{Code: rec.Codes[0]})
	require.NoError(t, err)
	assert.Len(t, fresh.Codes, 10)
	err = h.client.Mfa.Disable(asBob, &mfa.MfaCodeRequest{Code: rec.Codes[1]})
	assert.Error(t, err, "old recovery codes died with the regeneration")

	// A non-admin cannot reset; an admin can, for a person of this tenant alone.
	_, err = h.client.Admin.ResetMfa(asBob, &UserIdRequest{Id: bob.ID.String()})
	assert.Equal(t, http.StatusForbidden, statusOf(err))
	_, err = h.client.Admin.ResetMfa(asAdmin, &UserIdRequest{Id: bob.ID.String()})
	require.NoError(t, err)
	st, _ = h.client.Mfa.Status(asBob)
	assert.False(t, st.Enabled)

	// A person in two tenants: the other tenant's admin cannot reset their MFA.
	other := uuid.New()
	_, _, err = identity.AddMember(context.Background(), h.pool, identity.NewMember{TenantID: other, Email: "bob@acme.io"})
	require.NoError(t, err)
	_, err = h.client.Admin.ResetMfa(asAdmin, &UserIdRequest{Id: bob.ID.String()})
	assert.Error(t, err)
}

// Wrong codes are counted per identity and survive the failed request (audit 07): after
// MaxMFAFailures even the right code is refused until the lock lapses, so a six-digit code cannot
// be guessed from a stolen session.
func TestWrongMFACodesLockTheFactor(t *testing.T) {
	h := newIdentityHarness(t)
	tenant := uuid.New()
	bob := h.member(tenant, "bob@acme.io")
	asBob := h.as(tenant, bob.ID)
	en, err := h.client.Mfa.Begin(asBob)
	require.NoError(t, err)
	good, err := identity.TOTPAt(en.Secret, time.Now())
	require.NoError(t, err)
	_, err = h.client.Mfa.Confirm(asBob, &mfa.MfaCodeRequest{Code: good})
	require.NoError(t, err)

	for i := 0; i < identity.MaxMFAFailures; i++ {
		err = h.client.Mfa.Disable(asBob, &mfa.MfaCodeRequest{Code: "000000"})
		require.Error(t, err)
	}
	next, err := identity.TOTPAt(en.Secret, time.Now().Add(30*time.Second))
	require.NoError(t, err)
	err = h.client.Mfa.Disable(asBob, &mfa.MfaCodeRequest{Code: next})
	assert.Equal(t, http.StatusTooManyRequests, statusOf(err), "the right code is refused while locked")
	st, _ := h.client.Mfa.Status(asBob)
	assert.True(t, st.Enabled, "a locked factor is still on")

	// The lock lapses: the right code works again, and the counter starts over.
	_, err = h.pool.Exec(context.Background(), `UPDATE metadata.mfa_factors SET locked_until = now() - interval '1 minute'
		WHERE identity_id = (SELECT identity_id FROM metadata.users WHERE id = $1)`, bob.ID)
	require.NoError(t, err)
	_, err = h.client.Mfa.RegenerateRecoveryCodes(asBob, &mfa.MfaCodeRequest{Code: good})
	assert.Error(t, err, "the code of the earlier step was used already")
	_, err = h.client.Mfa.RegenerateRecoveryCodes(asBob, &mfa.MfaCodeRequest{Code: next})
	assert.NoError(t, err)
}
