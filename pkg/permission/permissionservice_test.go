package permission

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type emptyStore struct{}

func (emptyStore) FindContextPolicies(context.Context, uuid.UUID, string, string) ([]PolicyRow, error) {
	return nil, nil
}
func (emptyStore) LoadAllPolicies(context.Context, uuid.UUID, string) ([]PolicyRow, error) {
	return nil, nil
}
func (emptyStore) FindExplainPolicies(context.Context, uuid.UUID, string, string, ExplainOptions) ([]PolicyRow, error) {
	return nil, nil
}
func (emptyStore) ListPolicies(context.Context, uuid.UUID, string, bool) ([]PolicyRow, error) {
	return nil, nil
}
func (emptyStore) CreatePolicy(context.Context, uuid.UUID, string, string, []string, bool,
	*PolicyCondition, *uuid.UUID, string, *PolicySubject, PolicyEffect) (PolicyRow, error) {
	panic("unimplemented")
}
func (emptyStore) DeletePolicy(context.Context, uuid.UUID, uuid.UUID) error {
	panic("unimplemented")
}
func (emptyStore) SyncBasicPolicies(context.Context, uuid.UUID, string, []RoleActionGrant, *uuid.UUID) ([]PolicyRow, error) {
	panic("unimplemented")
}

func adminContext(scope string, hasScope bool) *RequestContext {
	ctx := &RequestContext{TenantID: uuid.NewString(), UserID: uuid.NewString(), Roles: []string{"admin"}, HasRoles: true}
	if hasScope {
		ctx.ApplyOAuthGrant(scope, "client", true)
	}
	return ctx
}

func TestScopeNarrowsAnAdminUserButAnOrdinarySessionKeepsTheAdminBypass(t *testing.T) {
	service := NewPermissionService(emptyStore{})
	ctx := context.Background()

	session := adminContext("", false)
	decision, err := service.CanDeleteEntity(ctx, session, "waf.zones")
	require.NoError(t, err, "got (%v, %v)", decision, err)
	require.True(t, decision.Allowed, "got (%v, %v)", decision, err)

	scoped := adminContext("read:waf.zones write:waf.rules", true)
	must := func(d PermissionDecision, err error) bool {
		require.NoError(t, err)
		return d.Allowed
	}
	require.True(t, must(service.CanReadEntity(ctx, scoped, "waf.zones")), "expected allow")
	require.False(t, must(service.CanUpdateEntity(ctx, scoped, "waf.zones")), "expected deny")
	require.False(t, must(service.CanDeleteEntity(ctx, scoped, "waf.zones")), "expected deny")
	require.False(t, must(service.CanReadEntity(ctx, scoped, "waf.rules")), "expected deny")
	require.True(t, must(service.CanCreateEntity(ctx, scoped, "waf.rules")), "expected allow")
	require.False(t, must(service.CanTransitionEntity(ctx, scoped, "waf.rules")), "expected deny")
}

func TestAnEmptyScopeDeniesEverythingEvenForAnAdminUser(t *testing.T) {
	service := NewPermissionService(emptyStore{})
	scoped := adminContext("", true)
	decision, err := service.CanReadEntity(context.Background(), scoped, "waf.zones")
	require.NoError(t, err)
	require.False(t, decision.Allowed, "expected deny")
}
