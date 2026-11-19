//go:build integration

package grpcapi

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/metap-org/metap/pkg/grpcapi/pb"
	"github.com/metap-org/metap/pkg/identity"
)

func TestAccountMFALifecycleAndAdminReset(t *testing.T) {
	h := newIdentityHarness(t)
	tenant := uuid.New()
	admin := h.member(tenant, "admin@acme.io", "admin")
	bob := h.member(tenant, "bob@acme.io")
	asBob, asAdmin := h.as(tenant, bob.ID), h.as(tenant, admin.ID)

	st, err := h.client.Account.GetMfaStatus(asBob, &pb.Empty{})
	require.NoError(t, err)
	assert.False(t, st.GetEnabled())

	// Confirming before starting, or with a wrong code, fails.
	_, err = h.client.Account.ConfirmMfaEnrollment(asBob, &pb.MfaCodeRequest{Code: "123456"})
	assert.Error(t, err)
	en, err := h.client.Account.BeginMfaEnrollment(asBob, &pb.Empty{})
	require.NoError(t, err)
	assert.Contains(t, en.GetOtpauthUri(), "otpauth://totp/")
	assert.Equal(t, "mfa_invalid_code", errCode(t, func(o grpc.CallOption) error {
		_, err := h.client.Account.ConfirmMfaEnrollment(asBob, &pb.MfaCodeRequest{Code: "000000"}, o)
		return err
	}))
	code, err := identity.TOTPAt(en.GetSecret(), time.Now())
	require.NoError(t, err)
	rec, err := h.client.Account.ConfirmMfaEnrollment(asBob, &pb.MfaCodeRequest{Code: code})
	require.NoError(t, err)
	require.Len(t, rec.GetCodes(), 10)
	st, _ = h.client.Account.GetMfaStatus(asBob, &pb.Empty{})
	assert.True(t, st.GetEnabled())
	assert.EqualValues(t, 10, st.GetRecoveryCodesLeft())

	// Starting over while on is refused; turning off needs a valid code.
	assert.Equal(t, "mfa_already_on", errCode(t, func(o grpc.CallOption) error {
		_, err := h.client.Account.BeginMfaEnrollment(asBob, &pb.Empty{}, o)
		return err
	}))
	_, err = h.client.Account.DisableMfa(asBob, &pb.MfaCodeRequest{Code: "000000"})
	assert.Error(t, err)

	// A recovery code proves it too, and is used up; new codes replace the old ones.
	fresh, err := h.client.Account.RegenerateRecoveryCodes(asBob, &pb.MfaCodeRequest{Code: rec.GetCodes()[0]})
	require.NoError(t, err)
	assert.Len(t, fresh.GetCodes(), 10)
	_, err = h.client.Account.DisableMfa(asBob, &pb.MfaCodeRequest{Code: rec.GetCodes()[1]})
	assert.Error(t, err, "old recovery codes died with the regeneration")

	// A non-admin cannot reset; an admin can, for a person of this tenant alone.
	_, err = h.client.Admin.ResetMfa(asBob, &pb.UserIdRequest{Id: bob.ID.String()})
	assert.Equal(t, codes.PermissionDenied, status.Code(err))
	_, err = h.client.Admin.ResetMfa(asAdmin, &pb.UserIdRequest{Id: bob.ID.String()})
	require.NoError(t, err)
	st, _ = h.client.Account.GetMfaStatus(asBob, &pb.Empty{})
	assert.False(t, st.GetEnabled())

	// A person in two tenants: the other tenant's admin cannot reset their MFA.
	other := uuid.New()
	_, _, err = identity.AddMember(context.Background(), h.pool, identity.NewMember{TenantID: other, Email: "bob@acme.io"})
	require.NoError(t, err)
	_, err = h.client.Admin.ResetMfa(asAdmin, &pb.UserIdRequest{Id: bob.ID.String()})
	assert.Error(t, err)
}
