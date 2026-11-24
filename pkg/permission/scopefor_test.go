package permission

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/kit/scope"
)

func scopeForFixture(t *testing.T, grants map[string][]string) (*PermissionService, string) {
	t.Helper()
	res := declareTestResource(t)
	rows := map[string][]PolicyRow{}
	for action, roles := range grants {
		rows[ResourceKey(res)+" "+action] = []PolicyRow{allowFor(roles...)}
	}
	return NewPermissionService(resourceStore{rows: rows}), res
}

func callerWith(tenant uuid.UUID, roles []string, scoped ...ScopedRole) *RequestContext {
	return &RequestContext{TenantID: tenant.String(), UserID: uuid.NewString(), Roles: roles, HasRoles: true, ScopedRoles: scoped}
}

func TestAnAdminOrATenantWideRoleSeesTheWholeTenant(t *testing.T) {
	s, res := scopeForFixture(t, map[string][]string{"view": {"analyst"}})
	tenant := uuid.New()
	for _, roles := range [][]string{{"admin"}, {"analyst"}} {
		got, d, err := s.ScopeFor(context.Background(), callerWith(tenant, roles), res, "view")
		require.NoError(t, err)
		require.True(t, d.Allowed)
		require.NoError(t, got.Valid())
		require.Equal(t, tenant.String(), got.Tenant())
		require.Empty(t, got.Limits(), "no limit: every zone of the tenant")
	}
}

func TestARoleHeldOnlyOnSomeZonesSeesOnlyThoseZones(t *testing.T) {
	s, res := scopeForFixture(t, map[string][]string{"view": {"zone_analyst"}})
	tenant := uuid.New()
	rc := callerWith(tenant, []string{"member"},
		ScopedRole{Role: "zone_analyst", ScopeType: "zone", ScopeID: "z1"},
		ScopedRole{Role: "zone_analyst", ScopeType: "zone", ScopeID: "z2"},
		ScopedRole{Role: "other_role", ScopeType: "zone", ScopeID: "z3"}, // a role the policy does not grant
	)
	got, d, err := s.ScopeFor(context.Background(), rc, res, "view")
	require.NoError(t, err)
	require.True(t, d.Allowed)
	require.Equal(t, map[string][]string{"zone": {"z1", "z2"}}, got.Limits())
	require.True(t, got.Allows("zone", "z1"))
	require.False(t, got.Allows("zone", "z3"), "a zone where only an ungranted role is held")
	require.False(t, got.Allows("zone", "z9"))
}

func TestACallerWithNoGrantAnywhereIsRefusedAndGetsNoScope(t *testing.T) {
	s, res := scopeForFixture(t, map[string][]string{"view": {"analyst"}})
	rc := callerWith(uuid.New(), []string{"viewer"}, ScopedRole{Role: "viewer", ScopeType: "zone", ScopeID: "z1"})
	got, d, err := s.ScopeFor(context.Background(), rc, res, "view")
	require.NoError(t, err)
	require.False(t, d.Allowed)
	require.ErrorIs(t, got.Valid(), scope.ErrInvalid, "the refusal comes with the zero Scope, which no adapter accepts")
}

func TestAGrantOnOneActionSaysNothingAboutAnother(t *testing.T) {
	s, res := scopeForFixture(t, map[string][]string{"view": {"analyst"}})
	_, d, err := s.ScopeFor(context.Background(), callerWith(uuid.New(), []string{"analyst"}), res, "approve")
	require.NoError(t, err)
	require.False(t, d.Allowed)
}

func TestScopesOfSeveralTypesNarrowTogether(t *testing.T) {
	s, res := scopeForFixture(t, map[string][]string{"view": {"scoped"}})
	rc := callerWith(uuid.New(), nil,
		ScopedRole{Role: "scoped", ScopeType: "zone", ScopeID: "z1"},
		ScopedRole{Role: "scoped", ScopeType: "account", ScopeID: "a1"},
	)
	got, d, err := s.ScopeFor(context.Background(), rc, res, "view")
	require.NoError(t, err)
	require.True(t, d.Allowed)
	require.Equal(t, map[string][]string{"zone": {"z1"}, "account": {"a1"}}, got.Limits())
}

func TestAnUndeclaredResourceIsAnErrorNotAnEmptyScope(t *testing.T) {
	s := NewPermissionService(resourceStore{})
	_, _, err := s.ScopeFor(context.Background(), callerWith(uuid.New(), []string{"admin"}), "test.never_declared", "view")
	require.Error(t, err)
}
