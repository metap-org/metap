//go:build integration

package grpcapi

import (
	"context"
	"regexp"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/metap-org/metap/pkg/control"
	"github.com/metap-org/metap/pkg/grpcapi/pb"
	"github.com/metap-org/metap/pkg/identity"
	"github.com/metap-org/metap/pkg/peripherals"
)

func TestTenantAdminService(t *testing.T) {
	h := newIdentityHarness(t)
	ctx := context.Background()
	platform := h.member(control.PlatformTenantID, "root@platform.io", RolePlatformAdmin)
	asPlatform := h.as(control.PlatformTenantID, platform.ID)

	// A tenant's own admin is not a platform admin.
	acme := uuid.New()
	acmeAdmin := h.member(acme, "admin@acme.io", "admin")
	assert.Equal(t, "forbidden", errCode(t, func(o grpc.CallOption) error {
		_, err := h.client.Tenants.ListTenants(h.as(acme, acmeAdmin.ID), &pb.ListTenantsRequest{}, o)
		return err
	}))
	_, err := h.client.Tenants.ListTenants(context.Background(), &pb.ListTenantsRequest{})
	assert.Equal(t, codes.Unauthenticated, status.Code(err))

	// Validation.
	assert.Equal(t, "validation_failed", errCode(t, func(o grpc.CallOption) error {
		_, err := h.client.Tenants.CreateTenant(asPlatform, &pb.CreateTenantRequest{Name: "  ", AdminEmail: "a@b.io"}, o)
		return err
	}))
	assert.Equal(t, "validation_failed", errCode(t, func(o grpc.CallOption) error {
		_, err := h.client.Tenants.CreateTenant(asPlatform, &pb.CreateTenantRequest{Name: "X", AdminEmail: "not-an-email"}, o)
		return err
	}))

	// Create: the tenant is active, its admin is invited with the admin role, and the email goes out.
	created, err := h.client.Tenants.CreateTenant(asPlatform, &pb.CreateTenantRequest{
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
	roles, err := peripherals.GetRolesForUser(ctx, h.pool, tid, m.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{"admin"}, roles)

	// The new tenant is routable: Router opens a transaction on it.
	tx, err := h.router().Begin(ctx, tid)
	require.NoError(t, err)
	require.NoError(t, tx.Rollback(ctx))

	// Update.
	newName := "Globex International"
	upd, err := h.client.Tenants.UpdateTenant(asPlatform, &pb.UpdateTenantRequest{Id: tid.String(), Name: &newName})
	require.NoError(t, err)
	assert.Equal(t, newName, upd.GetName())
	assert.Equal(t, "trial", upd.GetTier())
	blank := ""
	assert.Equal(t, "validation_failed", errCode(t, func(o grpc.CallOption) error {
		_, err := h.client.Tenants.UpdateTenant(asPlatform, &pb.UpdateTenantRequest{Id: tid.String(), Name: &blank}, o)
		return err
	}))

	// List and search.
	list, err := h.client.Tenants.ListTenants(asPlatform, &pb.ListTenantsRequest{Query: "globex"})
	require.NoError(t, err)
	require.Equal(t, int32(1), list.GetTotal())
	assert.Equal(t, tid.String(), list.GetTenants()[0].GetId())

	// Suspend / resume; a suspended tenant is refused by Router.
	st, err := h.client.Tenants.SuspendTenant(asPlatform, &pb.TenantIdRequest{Id: tid.String()})
	require.NoError(t, err)
	assert.Equal(t, "suspended", st.GetStatus())
	_, err = h.router().Begin(ctx, tid)
	assert.Error(t, err, "Router refuses a suspended tenant")
	assert.Equal(t, "tenant_invalid_state", errCode(t, func(o grpc.CallOption) error {
		_, err := h.client.Tenants.SuspendTenant(asPlatform, &pb.TenantIdRequest{Id: tid.String()}, o)
		return err
	}))
	st, err = h.client.Tenants.ResumeTenant(asPlatform, &pb.TenantIdRequest{Id: tid.String()})
	require.NoError(t, err)
	assert.Equal(t, "active", st.GetStatus())

	// Deletion: suspended at once, cancellable, and a request can't be made twice.
	del, err := h.client.Tenants.RequestTenantDeletion(asPlatform, &pb.RequestTenantDeletionRequest{Id: tid.String(), RetentionDays: 7})
	require.NoError(t, err)
	assert.Equal(t, "suspended", del.GetStatus())
	assert.NotEmpty(t, del.GetDeletionEffectiveAt())
	assert.Equal(t, "tenant_invalid_state", errCode(t, func(o grpc.CallOption) error {
		_, err := h.client.Tenants.RequestTenantDeletion(asPlatform, &pb.RequestTenantDeletionRequest{Id: tid.String()}, o)
		return err
	}))
	back, err := h.client.Tenants.CancelTenantDeletion(asPlatform, &pb.TenantIdRequest{Id: tid.String()})
	require.NoError(t, err)
	assert.Equal(t, "active", back.GetStatus())
	assert.Empty(t, back.GetDeletionRequestedAt())

	// Unknown tenant.
	assert.Equal(t, "not_found", errCode(t, func(o grpc.CallOption) error {
		_, err := h.client.Tenants.GetTenant(asPlatform, &pb.TenantIdRequest{Id: uuid.NewString()}, o)
		return err
	}))

	// Every change was audited under the tenant entity, by the platform admin.
	var n int
	require.NoError(t, h.pool.QueryRow(ctx, `SELECT count(*) FROM metadata.audit_trail_entries
		WHERE entity = $1 AND record_id = $2 AND actor_user_id = $3`, AuditEntityTenants, tid, platform.ID).Scan(&n))
	assert.GreaterOrEqual(t, n, 6, "create, update, suspend, resume, request and cancel deletion")
}

// router is a fresh Router (so no cached registry entry from an earlier status).
func (h *identityHarness) router() *control.Router { return grpcTestRouterFor(h.pool) }
