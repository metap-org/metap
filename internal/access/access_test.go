package access

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/permission"
	"github.com/metap-org/metap/pkg/tenancy"
)

func caller(tenant uuid.UUID, roles ...string) *permission.RequestContext {
	return &permission.RequestContext{TenantID: tenant.String(), UserID: uuid.NewString(), Roles: roles, HasRoles: true}
}

func TestEverySignedInCallerMayUseAnAuthenticatedOperation(t *testing.T) {
	require.Empty(t, Refusal(Authenticated, caller(uuid.New())))
	require.Empty(t, Refusal(Authenticated, caller(uuid.New(), "viewer")))
}

func TestAnAdminOperationNeedsTheAdminRole(t *testing.T) {
	tenant := uuid.New()
	require.Contains(t, Refusal(Admin, caller(tenant, "viewer")), "admin role")
	require.Contains(t, Refusal(Admin, caller(tenant)), "admin role")
	require.Empty(t, Refusal(Admin, caller(tenant, "admin")))
	require.Empty(t, Refusal(Admin, caller(tenant, "viewer", "admin")))
}

func TestADurableOperationNeedsAnAdminToo(t *testing.T) {
	tenant := uuid.New()
	require.NotEmpty(t, Refusal(Durable, caller(tenant, "viewer")))
	require.Empty(t, Refusal(Durable, caller(tenant, "admin")), "the support-session half is the database's to check")
}

func TestAScopedTokenNeedsTheAdminScopeEvenForAnAdmin(t *testing.T) {
	rc := caller(uuid.New(), "admin")
	rc.ApplyOAuthGrant("read:waf.zones", "client-1", true)
	require.Contains(t, Refusal(Admin, rc), "OAuth2 scope")
	rc.ApplyOAuthGrant("admin", "client-1", true)
	require.Empty(t, Refusal(Admin, rc))
}

func TestPlatformAdminIsTheRoleOfThePlatformTenantOnly(t *testing.T) {
	platform := tenancy.PlatformTenantID
	require.Empty(t, Refusal(PlatformAdmin, caller(platform, RolePlatformAdmin)))
	require.Contains(t, Refusal(PlatformAdmin, caller(platform, "admin")), "platform_admin")
	require.Contains(t, Refusal(PlatformAdmin, caller(uuid.New(), RolePlatformAdmin)), "platform_admin",
		"the role in an ordinary tenant is not the platform's")
}

func TestLevelsHaveNames(t *testing.T) {
	require.Equal(t, "admin", Admin.String())
	require.Equal(t, "platformAdmin", PlatformAdmin.String())
}
