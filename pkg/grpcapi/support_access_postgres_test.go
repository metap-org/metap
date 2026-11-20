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

// Roadmap 115 M10: support staff act in a tenant only under a time-boxed, reasoned, revocable
// grant, as an attributable "Support: <email>" account that holds just the granted role.
func TestSupportAccessLifecycle(t *testing.T) {
	h := newIdentityHarness(t)
	tenant := uuid.New()
	admin := h.member(tenant, "admin@acme.io", "admin")
	bob := h.member(tenant, "bob@acme.io", "viewer")
	h.policyRole(tenant, "support-readonly")
	asAdmin := h.as(tenant, admin.ID)
	vendor := h.member(control.PlatformTenantID, "agent@vendor.io", RolePlatformAdmin)
	asVendor := h.as(control.PlatformTenantID, vendor.ID)

	// No grant yet: support cannot start a session.
	_, err := h.client.Support.StartSupportSession(asVendor, &pb.StartSupportSessionRequest{TenantId: tenant.String()})
	assert.Equal(t, codes.Aborted, status.Code(err), "%v", err)

	// Only a tenant admin grants, with a reason and an existing role.
	_, err = h.client.Support.GrantSupportAccess(h.as(tenant, bob.ID), &pb.GrantSupportAccessRequest{Reason: "x", Role: "viewer"})
	assert.Equal(t, codes.PermissionDenied, status.Code(err))
	_, err = h.client.Support.GrantSupportAccess(asAdmin, &pb.GrantSupportAccessRequest{Reason: "", Role: "support-readonly"})
	assert.Equal(t, codes.InvalidArgument, status.Code(err), "a reason is required")
	_, err = h.client.Support.GrantSupportAccess(asAdmin, &pb.GrantSupportAccessRequest{Reason: "ticket 42", Role: "no-such-role"})
	assert.Equal(t, codes.InvalidArgument, status.Code(err), "the role must exist")
	grant, err := h.client.Support.GrantSupportAccess(asAdmin, &pb.GrantSupportAccessRequest{Reason: "ticket 42", Role: "support-readonly", Hours: 2})
	require.NoError(t, err)
	assert.True(t, grant.GetActive())

	// A tenant admin cannot start a session (platform admins only); a platform admin can.
	_, err = h.client.Support.StartSupportSession(asAdmin, &pb.StartSupportSessionRequest{TenantId: tenant.String()})
	assert.Equal(t, codes.PermissionDenied, status.Code(err))
	session, err := h.client.Support.StartSupportSession(asVendor, &pb.StartSupportSessionRequest{TenantId: tenant.String()})
	require.NoError(t, err)
	assert.Equal(t, "support-readonly", session.GetRole())
	assert.NotEmpty(t, session.GetToken())

	// The key works as the support account: it holds the granted role only — not admin.
	asSupport := CallContext(context.Background(), session.GetToken())
	_, err = h.client.Admin.ListUsers(asSupport, &pb.ListUsersRequest{})
	assert.Equal(t, codes.PermissionDenied, status.Code(err), "support holds the granted role, not admin")
	sas, err := h.client.Admin.ListServiceAccounts(asAdmin, &pb.Empty{})
	require.NoError(t, err)
	require.Len(t, sas.GetUsers(), 1, "the tenant sees who is in")
	assert.Equal(t, "Support: agent@vendor.io", sas.GetUsers()[0].GetDisplayName())

	// Grants and sessions are visible, and both steps are in the tenant's audit trail.
	list, err := h.client.Support.ListSupportGrants(asAdmin, &pb.Empty{})
	require.NoError(t, err)
	require.Len(t, list.GetGrants(), 1)
	require.Len(t, list.GetGrants()[0].GetSessions(), 1)
	assert.Equal(t, "agent@vendor.io", list.GetGrants()[0].GetSessions()[0].GetStartedByEmail())
	trail, err := h.client.Audit.SearchAudit(asAdmin, &pb.SearchAuditRequest{Entity: AuditEntitySupportGrants})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(trail.GetEntries()), 2, "grant + session start")
	byVendor, err := h.client.Audit.SearchAudit(asAdmin, &pb.SearchAuditRequest{ActorUserId: vendor.ID.String()})
	require.NoError(t, err)
	assert.NotEmpty(t, byVendor.GetEntries(), "the session start is attributed to the support person")

	// Revoking ends it at once: the key stops working and the account is gone.
	revoked, err := h.client.Support.RevokeSupportGrant(asAdmin, &pb.SupportGrantIdRequest{Id: grant.GetId()})
	require.NoError(t, err)
	assert.False(t, revoked.GetActive())
	assert.NotEmpty(t, revoked.GetRevokedAt())
	_, err = h.client.Admin.ListServiceAccounts(asSupport, &pb.Empty{})
	assert.Equal(t, codes.Unauthenticated, status.Code(err), "the key is revoked")
	sas, err = h.client.Admin.ListServiceAccounts(asAdmin, &pb.Empty{})
	require.NoError(t, err)
	assert.Empty(t, sas.GetUsers())
	_, err = h.client.Support.StartSupportSession(asVendor, &pb.StartSupportSessionRequest{TenantId: tenant.String(), GrantId: grant.GetId()})
	assert.Equal(t, codes.Aborted, status.Code(err), "a revoked grant cannot start a session")
}

func TestSupportGrantExpires(t *testing.T) {
	h := newIdentityHarness(t)
	tenant := uuid.New()
	admin := h.member(tenant, "admin@acme.io", "admin")
	asAdmin := h.as(tenant, admin.ID)
	vendor := h.member(control.PlatformTenantID, "agent@vendor.io", RolePlatformAdmin)
	asVendor := h.as(control.PlatformTenantID, vendor.ID)
	grant, err := h.client.Support.GrantSupportAccess(asAdmin, &pb.GrantSupportAccessRequest{Reason: "r", Role: "admin", Hours: 1})
	require.NoError(t, err)
	_, err = h.pool.Exec(context.Background(), `UPDATE metadata.support_grants SET expires_at = now() - interval '1 minute' WHERE id = $1`, grant.GetId())
	require.NoError(t, err)
	_, err = h.client.Support.StartSupportSession(asVendor, &pb.StartSupportSessionRequest{TenantId: tenant.String(), GrantId: grant.GetId()})
	assert.Equal(t, codes.Aborted, status.Code(err), "an expired grant cannot start a session")
}
