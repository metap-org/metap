//go:build integration

package admin

import (
	"context"
	"github.com/metap-org/metap/internal/admin/apikeys"
	"github.com/metap-org/metap/internal/admin/support"
	"github.com/metap-org/metap/internal/admin/trail"
	"github.com/metap-org/metap/internal/admin/users"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/tenancy"
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
	vendor := h.member(tenancy.PlatformTenantID, "agent@vendor.io", RolePlatformAdmin)
	asVendor := h.as(tenancy.PlatformTenantID, vendor.ID)

	// No grant yet: support cannot start a session.
	_, err := h.client.Support.Start(asVendor, &support.StartSupportSessionRequest{TenantId: tenant.String()})
	assert.Equal(t, http.StatusConflict, statusOf(err), "%v", err)

	// Only a tenant admin grants, with a reason and an existing role.
	_, err = h.client.Support.Grant(h.as(tenant, bob.ID), &support.GrantSupportAccessRequest{Reason: "x", Role: "viewer"})
	assert.Equal(t, http.StatusForbidden, statusOf(err))
	_, err = h.client.Support.Grant(asAdmin, &support.GrantSupportAccessRequest{Reason: "", Role: "support-readonly"})
	assert.Equal(t, http.StatusBadRequest, statusOf(err), "a reason is required")
	_, err = h.client.Support.Grant(asAdmin, &support.GrantSupportAccessRequest{Reason: "ticket 42", Role: "no-such-role"})
	assert.Equal(t, http.StatusBadRequest, statusOf(err), "the role must exist")
	grant, err := h.client.Support.Grant(asAdmin, &support.GrantSupportAccessRequest{Reason: "ticket 42", Role: "support-readonly", Hours: 2})
	require.NoError(t, err)
	assert.True(t, grant.Active)

	// A tenant admin cannot start a session (platform admins only); a platform admin can.
	_, err = h.client.Support.Start(asAdmin, &support.StartSupportSessionRequest{TenantId: tenant.String()})
	assert.Equal(t, http.StatusForbidden, statusOf(err))
	session, err := h.client.Support.Start(asVendor, &support.StartSupportSessionRequest{TenantId: tenant.String()})
	require.NoError(t, err)
	assert.Equal(t, "support-readonly", session.Role)
	assert.NotEmpty(t, session.Token)

	// The key works as the support account: it holds the granted role only — not admin.
	asSupport := CallContext(context.Background(), session.Token)
	_, err = h.client.Admin.List(asSupport, &users.ListUsersRequest{})
	assert.Equal(t, http.StatusForbidden, statusOf(err), "support holds the granted role, not admin")
	sas, err := h.client.Admin.ListServiceAccounts(asAdmin, &Empty{})
	require.NoError(t, err)
	require.Len(t, sas.Users, 1, "the tenant sees who is in")
	assert.Equal(t, "Support: agent@vendor.io", sas.Users[0].DisplayName)

	// Grants and sessions are visible, and both steps are in the tenant's audit trail.
	list, err := h.client.Support.List(asAdmin)
	require.NoError(t, err)
	require.Len(t, list.Grants, 1)
	require.Len(t, list.Grants[0].Sessions, 1)
	assert.Equal(t, "agent@vendor.io", list.Grants[0].Sessions[0].StartedByEmail)
	h.drainAudit()
	found, err := h.client.Trail.Search(asAdmin, &trail.SearchAuditRequest{Entity: support.AuditEntity})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(found.Entries), 2, "grant + session start")
	h.drainAudit()
	byVendor, err := h.client.Trail.Search(asAdmin, &trail.SearchAuditRequest{ActorUserId: vendor.ID.String()})
	require.NoError(t, err)
	assert.NotEmpty(t, byVendor.Entries, "the session start is attributed to the support person")

	// Revoking ends it at once: the key stops working and the account is gone.
	revoked, err := h.client.Support.Revoke(asAdmin, &support.SupportGrantIdRequest{Id: grant.Id})
	require.NoError(t, err)
	assert.False(t, revoked.Active)
	assert.NotEmpty(t, revoked.RevokedAt)
	_, err = h.client.Admin.ListServiceAccounts(asSupport, &Empty{})
	assert.Equal(t, http.StatusUnauthorized, statusOf(err), "the key is revoked")
	sas, err = h.client.Admin.ListServiceAccounts(asAdmin, &Empty{})
	require.NoError(t, err)
	assert.Empty(t, sas.Users)
	_, err = h.client.Support.Start(asVendor, &support.StartSupportSessionRequest{TenantId: tenant.String(), GrantId: grant.Id})
	assert.Equal(t, http.StatusConflict, statusOf(err), "a revoked grant cannot start a session")
}

func TestSupportGrantExpires(t *testing.T) {
	h := newIdentityHarness(t)
	tenant := uuid.New()
	admin := h.member(tenant, "admin@acme.io", "admin")
	asAdmin := h.as(tenant, admin.ID)
	vendor := h.member(tenancy.PlatformTenantID, "agent@vendor.io", RolePlatformAdmin)
	asVendor := h.as(tenancy.PlatformTenantID, vendor.ID)
	grant, err := h.client.Support.Grant(asAdmin, &support.GrantSupportAccessRequest{Reason: "r", Role: "admin", Hours: 1})
	require.NoError(t, err)
	_, err = h.pool.Exec(context.Background(), `UPDATE metadata.support_grants SET expires_at = now() - interval '1 minute' WHERE id = $1`, grant.Id)
	require.NoError(t, err)
	_, err = h.client.Support.Start(asVendor, &support.StartSupportSessionRequest{TenantId: tenant.String(), GrantId: grant.Id})
	assert.Equal(t, http.StatusConflict, statusOf(err), "an expired grant cannot start a session")
}

// A support session holding the admin role still cannot create access that outlives it: a service
// account, a key, a person, a role, a sign-in provider. Otherwise revoking the grant would leave
// whatever the session made behind.
func TestSupportSessionCannotCreateDurableAccess(t *testing.T) {
	h := newIdentityHarness(t)
	tenant := uuid.New()
	admin := h.member(tenant, "admin@acme.io", "admin")
	bob := h.member(tenant, "bob@acme.io", "viewer")
	asAdmin := h.as(tenant, admin.ID)
	vendor := h.member(tenancy.PlatformTenantID, "agent@vendor.io", RolePlatformAdmin)
	asVendor := h.as(tenancy.PlatformTenantID, vendor.ID)

	_, err := h.client.Support.Grant(asAdmin, &support.GrantSupportAccessRequest{Reason: "outage", Role: "admin", Hours: 2})
	require.NoError(t, err)
	session, err := h.client.Support.Start(asVendor, &support.StartSupportSessionRequest{TenantId: tenant.String()})
	require.NoError(t, err)
	asSupport := CallContext(context.Background(), session.Token)

	_, err = h.client.Admin.List(asSupport, &users.ListUsersRequest{})
	assert.NoError(t, err, "it can look around and do the admin's ordinary work")

	_, err = h.client.Admin.CreateServiceAccount(asSupport, &users.CreateServiceAccountRequest{Name: "backdoor"})
	assert.Equal(t, http.StatusForbidden, statusOf(err), "no service account")
	_, err = h.client.Keys.Create(asSupport, &apikeys.CreateApiKeyRequest{UserId: bob.ID.String(), Name: "k", ExpiresInDays: 30})
	assert.Equal(t, http.StatusForbidden, statusOf(err), "no key")
	_, err = h.client.Admin.Invite(asSupport, &users.InviteUserRequest{Email: "mallory@evil.test", Roles: []string{"admin"}})
	assert.Equal(t, http.StatusForbidden, statusOf(err), "no invitation")
	_, err = h.client.Admin.AssignRole(asSupport, &users.RoleRequest{Id: bob.ID.String(), Role: "admin"})
	assert.Equal(t, http.StatusForbidden, statusOf(err), "no role")
	_, err = h.client.Admin.ResetPassword(asSupport, &users.ResetPasswordRequest{Id: bob.ID.String(), Password: "a-new-password-123"})
	assert.Equal(t, http.StatusForbidden, statusOf(err), "no password reset")
	_, err = h.client.Admin.ResetMfa(asSupport, &users.UserIdRequest{Id: bob.ID.String()})
	assert.Equal(t, http.StatusForbidden, statusOf(err), "no MFA reset")

	// The tenant's own admin is unaffected.
	_, err = h.client.Admin.CreateServiceAccount(asAdmin, &users.CreateServiceAccountRequest{Name: "ci"})
	assert.NoError(t, err)
}
