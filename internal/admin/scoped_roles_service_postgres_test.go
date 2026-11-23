//go:build integration

package admin

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/peripherals"
)

func TestScopedRoleAdministration(t *testing.T) {
	h := newIdentityHarness(t)
	ctx := context.Background()
	tenant := uuid.New()
	admin := h.member(tenant, "admin@acme.io", "admin")
	bob := h.member(tenant, "bob@acme.io")
	h.policyRole(tenant, "analyst")
	asAdmin := h.as(tenant, admin.ID)
	zone := uuid.NewString()

	// Assign on a scope: the user shows it, apart from tenant-wide roles.
	u, err := h.client.Admin.AssignRole(asAdmin, &RoleRequest{Id: bob.ID.String(), Role: "analyst", ScopeType: "zone", ScopeId: zone})
	require.NoError(t, err)
	assert.Empty(t, u.GetRoles(), "a scoped role is not a tenant-wide one")
	require.Len(t, u.GetScopedRoles(), 1)
	assert.Equal(t, "analyst", u.GetScopedRoles()[0].GetRole())
	assert.Equal(t, "zone", u.GetScopedRoles()[0].GetScopeType())
	assert.Equal(t, zone, u.GetScopedRoles()[0].GetScopeId())

	// What the request context for bob is built from.
	sr, err := peripherals.ScopedRolesForUser(ctx, h.pool, tenant, bob.ID)
	require.NoError(t, err)
	require.Len(t, sr, 1)
	roles, _ := peripherals.GetRolesForUser(ctx, h.pool, tenant, bob.ID)
	assert.Empty(t, roles)

	// A duplicate is a no-op.
	u, err = h.client.Admin.AssignRole(asAdmin, &RoleRequest{Id: bob.ID.String(), Role: "analyst", ScopeType: "zone", ScopeId: zone})
	require.NoError(t, err)
	assert.Len(t, u.GetScopedRoles(), 1)

	// Validation: the role must exist, the scope must be well formed, both scope fields together.
	assert.Equal(t, "validation_failed", errCode(t, func() error {
		_, err := h.client.Admin.AssignRole(asAdmin, &RoleRequest{Id: bob.ID.String(), Role: "ghost", ScopeType: "zone", ScopeId: zone})
		return err
	}))
	for _, bad := range []*RoleRequest{
		{Id: bob.ID.String(), Role: "analyst", ScopeType: "Zone!", ScopeId: zone},
		{Id: bob.ID.String(), Role: "analyst", ScopeType: "zone"},
		{Id: bob.ID.String(), Role: "analyst", ScopeId: zone},
	} {
		bad := bad
		assert.Equal(t, "validation_failed", errCode(t, func() error {
			_, err := h.client.Admin.AssignRole(asAdmin, bad)
			return err
		}), "%+v", bad)
	}

	// Who can see the zone.
	members, err := h.client.Admin.ListScopeMembers(asAdmin, &ScopeRequest{ScopeType: "zone", ScopeId: zone})
	require.NoError(t, err)
	require.Len(t, members.GetMembers(), 1)
	assert.Equal(t, "bob@acme.io", members.GetMembers()[0].GetEmail())
	assert.Equal(t, "analyst", members.GetMembers()[0].GetRole())

	// A scoped admin role is not the tenant's last admin: revoking it needs no guard, and the
	// real admin is untouched either way.
	_, err = h.client.Admin.AssignRole(asAdmin, &RoleRequest{Id: bob.ID.String(), Role: "admin", ScopeType: "zone", ScopeId: zone})
	require.NoError(t, err)
	_, err = h.client.Admin.RevokeRole(asAdmin, &RoleRequest{Id: bob.ID.String(), Role: "admin", ScopeType: "zone", ScopeId: zone})
	require.NoError(t, err)

	// Not an admin: forbidden.
	asBob := h.as(tenant, bob.ID)
	_, err = h.client.Admin.AssignRole(asBob, &RoleRequest{Id: bob.ID.String(), Role: "analyst", ScopeType: "zone", ScopeId: "z"})
	assert.Equal(t, http.StatusForbidden, statusOf(err), "a user cannot hand themselves a role")
	_, err = h.client.Admin.ListScopeMembers(asBob, &ScopeRequest{ScopeType: "zone", ScopeId: zone})
	assert.Equal(t, http.StatusForbidden, statusOf(err))

	// Audited, and revoking works.
	var n int
	require.NoError(t, h.pool.QueryRow(ctx, `SELECT count(*) FROM metadata.audit_trail_entries
		WHERE entity = $1 AND record_id = $2 AND diff ? 'scopedRoles'`, AuditEntityUsers, bob.ID).Scan(&n))
	assert.GreaterOrEqual(t, n, 3, "assign, assign admin, revoke admin")
	u, err = h.client.Admin.RevokeRole(asAdmin, &RoleRequest{Id: bob.ID.String(), Role: "analyst", ScopeType: "zone", ScopeId: zone})
	require.NoError(t, err)
	assert.Empty(t, u.GetScopedRoles())

	// Removing a user takes their scoped roles with them.
	_, err = h.client.Admin.AssignRole(asAdmin, &RoleRequest{Id: bob.ID.String(), Role: "analyst", ScopeType: "zone", ScopeId: zone})
	require.NoError(t, err)
	_, err = h.client.Admin.RemoveUser(asAdmin, &UserIdRequest{Id: bob.ID.String()})
	require.NoError(t, err)
	sr, _ = peripherals.ScopedRolesForUser(ctx, h.pool, tenant, bob.ID)
	assert.Empty(t, sr)
}
