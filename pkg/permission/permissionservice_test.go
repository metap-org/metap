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

// CanRunAction: the tenant's policies on action:<name> decide when there are any; otherwise the roles
// the action declares; an action that declares none is open to any signed-in caller.
func TestCanRunActionPolicyThenDeclaredRolesThenOpen(t *testing.T) {
	tenant := uuid.New()
	rows := map[string][]PolicyRow{}
	store := rowStore{rows: rows}
	svc := NewPermissionService(store)
	caller := func(roles ...string) *RequestContext {
		return &RequestContext{TenantID: tenant.String(), UserID: uuid.NewString(), Roles: roles, HasRoles: true}
	}
	can := func(rc *RequestContext, declared ...string) bool {
		d, err := svc.CanRunAction(context.Background(), rc, "waf.zones", "verifyDns", declared)
		require.NoError(t, err)
		return d.Allowed
	}

	// No policy: the declared roles decide.
	require.True(t, can(caller("analyst"), "analyst", "developer"))
	require.False(t, can(caller("viewer"), "analyst", "developer"))
	// Nothing declared, no policy: open to anyone signed in.
	require.True(t, can(caller("viewer")))
	// The admin role always may, whatever is declared.
	require.True(t, can(caller("admin"), "analyst"))

	// The tenant gives viewer the action: its policy replaces the declared roles, for everyone.
	rows["waf.zones|action:verifyDns"] = []PolicyRow{{Entity: "waf.zones", Action: "action:verifyDns", Roles: []string{"viewer"}, HasRoles: true, Effect: EffectAllow}}
	require.True(t, can(caller("viewer"), "analyst"), "a policy grants it although the code did not declare the role")
	require.False(t, can(caller("analyst"), "analyst"), "and the declared role no longer counts once the tenant has policies")

	// An explicit deny wins over an allow.
	rows["waf.zones|action:verifyDns"] = append(rows["waf.zones|action:verifyDns"],
		PolicyRow{Entity: "waf.zones", Action: "action:verifyDns", Roles: []string{"viewer"}, HasRoles: true, Effect: EffectDeny})
	require.False(t, can(caller("viewer"), "analyst"))
}

func TestActionPolicyNameIsNotAnEntityActionOrATransition(t *testing.T) {
	require.Equal(t, "action:verifyDns", ActionPolicy("verifyDns"))
	require.False(t, IsEntityAction(ActionPolicy("verifyDns")))
	require.NotEqual(t, TransitionActionPolicy("verifyDns"), ActionPolicy("verifyDns"))
}

// rowStore serves FindContextPolicies from a map keyed "entity|action".
type rowStore struct {
	emptyStore
	rows map[string][]PolicyRow
}

func (r rowStore) FindContextPolicies(_ context.Context, _ uuid.UUID, entity, action string) ([]PolicyRow, error) {
	return r.rows[entity+"|"+action], nil
}
