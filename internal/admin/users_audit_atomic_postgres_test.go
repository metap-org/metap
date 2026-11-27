//go:build integration

package admin

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/internal/admin/users"
)

// Creating a service account, inviting someone and resetting a second factor each leave their entry
// in the trail: the change and its entry are one transaction, not a change followed by a best-effort
// write that a failure could drop.
func TestMembershipChangesLeaveTheirAuditEntry(t *testing.T) {
	h := newIdentityHarness(t)
	tenant := uuid.New()
	admin := h.member(tenant, "admin@acme.io", "admin")
	bob := h.member(tenant, "bob@acme.io")
	ctx := h.as(tenant, admin.ID)

	sa, err := h.client.Admin.CreateServiceAccount(ctx, &users.CreateServiceAccountRequest{Name: "ci-bot", Roles: []string{"admin"}})
	require.NoError(t, err)
	invited, err := h.client.Admin.Invite(ctx, &users.InviteUserRequest{Email: "new@acme.io", Roles: []string{"admin"}})
	require.NoError(t, err)
	_, err = h.client.Admin.ResetMfa(ctx, &users.UserIdRequest{Id: bob.ID.String()})
	require.NoError(t, err)
	h.drainAudit()

	for name, id := range map[string]string{
		"service account": sa.GetId(), "invitation": invited.GetUser().GetId(),
	} {
		trail, err := h.client.Admin.ListAudit(ctx, &users.ListUserAuditRequest{Id: id})
		require.NoError(t, err, name)
		require.Len(t, trail.GetEvents(), 1, name)
		assert.Equal(t, "create", trail.GetEvents()[0].GetAction(), name)
	}
	trail, err := h.client.Admin.ListAudit(ctx, &users.ListUserAuditRequest{Id: bob.ID.String()})
	require.NoError(t, err)
	require.Len(t, trail.GetEvents(), 1, "the reset is in the trail")
}
