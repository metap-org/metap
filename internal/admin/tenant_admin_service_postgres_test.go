//go:build integration

package admin

import (
	"context"
	"github.com/metap-org/metap/pkg/permission/rolestore"
	"net/http"
	"regexp"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/auth/identity"
	"github.com/metap-org/metap/pkg/tenancy"
)

func TestTenantAdminService(t *testing.T) {
	h := newIdentityHarness(t)
	ctx := context.Background()
	platform := h.member(tenancy.PlatformTenantID, "root@platform.io", RolePlatformAdmin)
	asPlatform := h.as(tenancy.PlatformTenantID, platform.ID)

	// A tenant's own admin is not a platform admin.
	acme := uuid.New()
	acmeAdmin := h.member(acme, "admin@acme.io", "admin")
	assert.Equal(t, "forbidden", errCode(t, func() error {
		_, err := h.client.Tenants.ListTenants(h.as(acme, acmeAdmin.ID), &ListTenantsRequest{})
		return err
	}))
	_, err := h.client.Tenants.ListTenants(context.Background(), &ListTenantsRequest{})
	assert.Equal(t, http.StatusUnauthorized, statusOf(err))

	// Validation.
	assert.Equal(t, "validation_failed", errCode(t, func() error {
		_, err := h.client.Tenants.CreateTenant(asPlatform, &CreateTenantRequest{Name: "  ", AdminEmail: "a@b.io"})
		return err
	}))
	assert.Equal(t, "validation_failed", errCode(t, func() error {
		_, err := h.client.Tenants.CreateTenant(asPlatform, &CreateTenantRequest{Name: "X", AdminEmail: "not-an-email"})
		return err
	}))

	// Create: the tenant is active, its admin is invited with the admin role, and the email goes out.
	created, err := h.client.Tenants.CreateTenant(asPlatform, &CreateTenantRequest{
		Name: "Globex", AdminEmail: "boss@globex.io", AdminDisplayName: "The Boss", Product: "waap"})
	require.NoError(t, err)
	tid := uuid.MustParse(created.GetTenant().GetId())
	assert.Equal(t, "Globex", created.GetTenant().GetName())
	assert.Equal(t, "trial", created.GetTenant().GetTier(), "default tier")
	assert.Equal(t, "active", created.GetTenant().GetStatus())
	assert.Equal(t, "waap", created.GetTenant().GetProduct())
	assert.Equal(t, int32(1), created.GetTenant().GetMemberCount())
	assert.True(t, created.GetEmailSent())
	assert.Empty(t, created.GetAcceptUrl())
	assert.Regexp(t, regexp.MustCompile(`http://portal.test/accept-invite\?token=`), h.mail.last(t, "boss@globex.io"))

	m, err := identity.GetMember(ctx, h.pool, tid, uuid.MustParse(created.GetAdminMemberId()))
	require.NoError(t, err)
	assert.Equal(t, identity.StatusInvited, m.Status)
	roles, err := rolestore.GetRolesForUser(ctx, h.pool, tid, m.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{"admin"}, roles)

	// The new tenant is routable: Router opens a transaction on it.
	tx, err := h.router().Begin(ctx, tid)
	require.NoError(t, err)
	require.NoError(t, tx.Rollback(ctx))

	// Update.
	newName := "Globex International"
	upd, err := h.client.Tenants.UpdateTenant(asPlatform, &UpdateTenantRequest{Id: tid.String(), Name: &newName})
	require.NoError(t, err)
	assert.Equal(t, newName, upd.GetName())
	assert.Equal(t, "trial", upd.GetTier())
	blank := ""
	assert.Equal(t, "validation_failed", errCode(t, func() error {
		_, err := h.client.Tenants.UpdateTenant(asPlatform, &UpdateTenantRequest{Id: tid.String(), Name: &blank})
		return err
	}))

	// List and search.
	list, err := h.client.Tenants.ListTenants(asPlatform, &ListTenantsRequest{Query: "globex"})
	require.NoError(t, err)
	require.Equal(t, int32(1), list.GetTotal())
	assert.Equal(t, tid.String(), list.GetTenants()[0].GetId())

	// Suspend / resume; a suspended tenant is refused by Router.
	st, err := h.client.Tenants.SuspendTenant(asPlatform, &TenantIdRequest{Id: tid.String()})
	require.NoError(t, err)
	assert.Equal(t, "suspended", st.GetStatus())
	_, err = h.router().Begin(ctx, tid)
	assert.Error(t, err, "Router refuses a suspended tenant")
	assert.Equal(t, "tenant_invalid_state", errCode(t, func() error {
		_, err := h.client.Tenants.SuspendTenant(asPlatform, &TenantIdRequest{Id: tid.String()})
		return err
	}))
	st, err = h.client.Tenants.ResumeTenant(asPlatform, &TenantIdRequest{Id: tid.String()})
	require.NoError(t, err)
	assert.Equal(t, "active", st.GetStatus())

	// Deletion: suspended at once, cancellable, and a request can't be made twice.
	del, err := h.client.Tenants.RequestTenantDeletion(asPlatform, &RequestTenantDeletionRequest{Id: tid.String(), RetentionDays: 7})
	require.NoError(t, err)
	assert.Equal(t, "suspended", del.GetStatus())
	assert.NotEmpty(t, del.GetDeletionEffectiveAt())
	assert.Equal(t, "tenant_invalid_state", errCode(t, func() error {
		_, err := h.client.Tenants.RequestTenantDeletion(asPlatform, &RequestTenantDeletionRequest{Id: tid.String()})
		return err
	}))
	back, err := h.client.Tenants.CancelTenantDeletion(asPlatform, &TenantIdRequest{Id: tid.String()})
	require.NoError(t, err)
	assert.Equal(t, "active", back.GetStatus())
	assert.Empty(t, back.GetDeletionRequestedAt())

	// Unknown tenant.
	assert.Equal(t, "not_found", errCode(t, func() error {
		_, err := h.client.Tenants.GetTenant(asPlatform, &TenantIdRequest{Id: uuid.NewString()})
		return err
	}))

	// Every change was audited under the tenant entity, by the platform admin.
	var n int
	require.NoError(t, h.pool.QueryRow(ctx, `SELECT count(*) FROM metadata.audit_trail_entries
		WHERE entity = $1 AND record_id = $2 AND actor_user_id = $3`, AuditEntityTenants, tid, platform.ID).Scan(&n))
	assert.GreaterOrEqual(t, n, 6, "create, update, suspend, resume, request and cancel deletion")
}

// router is a fresh Router (so no cached registry entry from an earlier status).
func (h *identityHarness) router() *tenancy.Router { return testRouterFor(h.pool) }
