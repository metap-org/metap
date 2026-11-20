//go:build integration

package grpcapi

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/metap-org/metap/pkg/control"
	"github.com/metap-org/metap/pkg/grpcapi/pb"
)

// Roadmap 115 M8: the owner is protected from other admins; ownership moves by the owner's own
// transfer or by a recovery the tenant's admins can cancel during the 72-hour wait.
func TestOwnerIsProtectedAndRecoveryWaits(t *testing.T) {
	h := newIdentityHarness(t)
	tenant := uuid.New()
	owner := h.member(tenant, "owner@acme.io", "admin")
	other := h.member(tenant, "admin2@acme.io", "admin")
	viewer := h.member(tenant, "viewer@acme.io", "viewer")
	asOwner, asOther := h.as(tenant, owner.ID), h.as(tenant, other.ID)
	vendor := h.member(control.PlatformTenantID, "agent@vendor.io", RolePlatformAdmin)
	asVendor := h.as(control.PlatformTenantID, vendor.ID)

	// The oldest admin is the owner, and the user list says so.
	st, err := h.client.Owner.GetOwnerStatus(asOther, &pb.Empty{})
	require.NoError(t, err)
	assert.Equal(t, owner.ID.String(), st.GetOwnerMemberId())
	users, err := h.client.Admin.ListUsers(asOwner, &pb.ListUsersRequest{})
	require.NoError(t, err)
	for _, u := range users.GetUsers() {
		assert.Equal(t, u.GetId() == owner.ID.String(), u.GetIsOwner(), u.GetEmail())
	}

	// Another admin cannot touch the owner.
	oid := &pb.UserIdRequest{Id: owner.ID.String()}
	_, err = h.client.Admin.DisableUser(asOther, oid)
	assert.Equal(t, codes.PermissionDenied, status.Code(err))
	_, err = h.client.Admin.RemoveUser(asOther, oid)
	assert.Equal(t, codes.PermissionDenied, status.Code(err))
	_, err = h.client.Admin.RevokeRole(asOther, &pb.RoleRequest{Id: owner.ID.String(), Role: "admin"})
	assert.Equal(t, codes.PermissionDenied, status.Code(err))
	_, err = h.client.Admin.ResetPassword(asOther, &pb.ResetPasswordRequest{Id: owner.ID.String(), Password: "Another-Passw0rd!"})
	assert.Equal(t, codes.PermissionDenied, status.Code(err))
	_, err = h.client.Admin.ResetMfa(asOther, oid)
	assert.Equal(t, codes.PermissionDenied, status.Code(err))
	// Ordinary members are still administered as before.
	_, err = h.client.Admin.DisableUser(asOther, &pb.UserIdRequest{Id: viewer.ID.String()})
	assert.NoError(t, err)

	// Only the owner transfers, and only to an active admin.
	_, err = h.client.Owner.TransferOwnership(asOther, &pb.TransferOwnershipRequest{MemberId: other.ID.String()})
	assert.Equal(t, codes.PermissionDenied, status.Code(err))
	_, err = h.client.Owner.TransferOwnership(asOwner, &pb.TransferOwnershipRequest{MemberId: viewer.ID.String()})
	assert.Equal(t, codes.InvalidArgument, status.Code(err), "not an admin")

	// Recovery: platform admin only, needs a reason, announces and waits.
	req := &pb.RequestOwnerRecoveryRequest{TenantId: tenant.String(), NewOwnerMemberId: other.ID.String(), Reason: "owner left the company"}
	_, err = h.client.Owner.RequestOwnerRecovery(asOwner, req)
	assert.Equal(t, codes.PermissionDenied, status.Code(err))
	_, err = h.client.Owner.RequestOwnerRecovery(asVendor, &pb.RequestOwnerRecoveryRequest{TenantId: tenant.String(), NewOwnerMemberId: other.ID.String()})
	assert.Equal(t, codes.InvalidArgument, status.Code(err), "a reason is required")
	rec, err := h.client.Owner.RequestOwnerRecovery(asVendor, req)
	require.NoError(t, err)
	assert.NotEmpty(t, rec.GetEffectiveAt())
	_, err = h.client.Owner.RequestOwnerRecovery(asVendor, req)
	assert.Equal(t, codes.Aborted, status.Code(err), "one recovery at a time")
	_, err = h.client.Owner.CompleteOwnerRecovery(asVendor, &pb.TenantIdOnlyRequest{TenantId: tenant.String()})
	assert.Equal(t, codes.Aborted, status.Code(err), "cannot complete during the 72-hour wait")
	st, err = h.client.Owner.GetOwnerStatus(asOwner, &pb.Empty{})
	require.NoError(t, err)
	assert.NotNil(t, st.GetPendingRecovery(), "the tenant sees the pending recovery")

	// An admin cancels it: nothing changes.
	st, err = h.client.Owner.CancelOwnerRecovery(asOther, &pb.Empty{})
	require.NoError(t, err)
	assert.Nil(t, st.GetPendingRecovery())
	assert.Equal(t, owner.ID.String(), st.GetOwnerMemberId())

	// A second recovery, left alone, completes after the wait and moves ownership.
	_, err = h.client.Owner.RequestOwnerRecovery(asVendor, req)
	require.NoError(t, err)
	_, err = h.pool.Exec(context.Background(),
		`UPDATE metadata.owner_recoveries SET effective_at = now() - interval '1 minute' WHERE tenant_id = $1 AND completed_at IS NULL AND cancelled_at IS NULL`, tenant)
	require.NoError(t, err)
	done, err := h.client.Owner.CompleteOwnerRecovery(asVendor, &pb.TenantIdOnlyRequest{TenantId: tenant.String()})
	require.NoError(t, err)
	assert.NotEmpty(t, done.GetCompletedAt())
	st, err = h.client.Owner.GetOwnerStatus(asOther, &pb.Empty{})
	require.NoError(t, err)
	assert.Equal(t, other.ID.String(), st.GetOwnerMemberId())
	// The old owner is now an ordinary admin, and the new owner is protected from them.
	_, err = h.client.Admin.DisableUser(asOwner, &pb.UserIdRequest{Id: other.ID.String()})
	assert.Equal(t, codes.PermissionDenied, status.Code(err))

	// The new owner can hand it back.
	back, err := h.client.Owner.TransferOwnership(asOther, &pb.TransferOwnershipRequest{MemberId: owner.ID.String()})
	require.NoError(t, err)
	assert.Equal(t, owner.ID.String(), back.GetOwnerMemberId())
}
