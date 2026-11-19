package permission

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func rec(kv map[string]any) JsonObject {
	out := JsonObject{}
	for k, v := range kv {
		b, _ := json.Marshal(v)
		out[k] = b
	}
	return out
}

func allow(action, role string) PolicyRow {
	return PolicyRow{Entity: "waf.rules", Action: action, Roles: []string{role}, HasRoles: true,
		Subject: "context", Effect: EffectAllow}
}

func scopedCtx(wide []string, scoped ...ScopedRole) *RequestContext {
	return &RequestContext{TenantID: "t", UserID: "u", Roles: wide, HasRoles: true, ScopedRoles: scoped}
}

var ruleScopes = []ScopeBinding{{Type: "zone", Field: "zoneId"}, {Type: "account", Field: "accountId"}, {Type: "group", Field: "groupIds", Array: true}}

func TestRoleGroupsMergeScopesWithTheSameRoleSet(t *testing.T) {
	ctx := scopedCtx([]string{"viewer"},
		ScopedRole{"analyst", "zone", "z1"}, ScopedRole{"analyst", "zone", "z2"}, ScopedRole{"editor", "zone", "z3"})
	groups := ctx.RoleGroups()
	assert.Len(t, groups, 3, "tenant-wide plus one per distinct role set")
	assert.True(t, groups[0].Wide())
	assert.Equal(t, []string{"viewer"}, groups[0].Roles)
	assert.Equal(t, []string{"analyst", "viewer"}, groups[1].Roles)
	assert.Equal(t, []ScopeRef{{"zone", "z1"}, {"zone", "z2"}}, groups[1].Scopes, "a thousand zones is one group")
	assert.Equal(t, []string{"editor", "viewer"}, groups[2].Roles)
	assert.Len(t, scopedCtx([]string{"a"}).RoleGroups(), 1, "no scoped roles: the tenant-wide group only")
}

func TestCoversBindings(t *testing.T) {
	g := RoleGroup{Scopes: []ScopeRef{{"zone", "z1"}, {"group", "g9"}}}
	assert.True(t, g.Covers(ruleScopes, "r1", rec(map[string]any{"zoneId": "z1"})))
	assert.False(t, g.Covers(ruleScopes, "r1", rec(map[string]any{"zoneId": "z2"})))
	assert.True(t, g.Covers(ruleScopes, "r1", rec(map[string]any{"zoneId": "z2", "groupIds": []string{"g1", "g9"}})), "array binding")
	assert.False(t, g.Covers(ruleScopes, "r1", rec(map[string]any{"groupIds": []string{"g1"}})))
	assert.False(t, g.Covers(ruleScopes, "r1", rec(map[string]any{})), "a record with no scope field is in no scope")
	assert.True(t, g.Covers([]ScopeBinding{{Type: "zone", Field: "id"}}, "z1", rec(map[string]any{})), "id binding: the record is the scope itself")
	assert.False(t, g.Covers([]ScopeBinding{{Type: "account", Field: "accountId"}}, "x", rec(map[string]any{"accountId": "z1"})), "type must match")
	assert.True(t, RoleGroup{}.Covers(nil, "", nil), "the tenant-wide group covers everything")
}

func TestCanAccessRecordRoleGrantedOnOneScopeDoesNotLeakToAnother(t *testing.T) {
	// analyst may update rules; viewer may only read. Both on the same entity with NO record policies,
	// which is exactly the case where the record-level check alone would allow everything.
	snap := PermissionSnapshotFromRows([]PolicyRow{allow("read", "analyst"), allow("update", "analyst"), allow("read", "viewer")})
	ctx := scopedCtx(nil, ScopedRole{"analyst", "zone", "z1"}, ScopedRole{"viewer", "zone", "z2"})

	in1 := rec(map[string]any{"zoneId": "z1"})
	in2 := rec(map[string]any{"zoneId": "z2"})
	elsewhere := rec(map[string]any{"zoneId": "z3"})
	can := func(r JsonObject, a EntityAction) bool {
		return snap.CanAccessRecord(ctx, "waf.rules", ruleScopes, "r", r, a, "").Allowed
	}
	assert.True(t, can(in1, ActionRead))
	assert.True(t, can(in1, ActionUpdate), "analyst on z1")
	assert.True(t, can(in2, ActionRead), "viewer on z2")
	assert.False(t, can(in2, ActionUpdate), "viewer on z2 must not inherit analyst's update from z1")
	assert.False(t, can(elsewhere, ActionRead), "outside every scope")
	assert.False(t, can(elsewhere, ActionUpdate))
}

func TestTenantWideRoleStillReachesEverything(t *testing.T) {
	snap := PermissionSnapshotFromRows([]PolicyRow{allow("read", "viewer"), allow("update", "analyst")})
	ctx := scopedCtx([]string{"viewer"}, ScopedRole{"analyst", "zone", "z1"})
	read := func(z string) bool {
		return snap.CanAccessRecord(ctx, "waf.rules", ruleScopes, "r", rec(map[string]any{"zoneId": z}), ActionRead, "").Allowed
	}
	update := func(z string) bool {
		return snap.CanAccessRecord(ctx, "waf.rules", ruleScopes, "r", rec(map[string]any{"zoneId": z}), ActionUpdate, "").Allowed
	}
	assert.True(t, read("z1"))
	assert.True(t, read("z9"), "viewer is tenant-wide")
	assert.True(t, update("z1"), "analyst scoped to z1")
	assert.False(t, update("z9"))
}

func TestScopedAdminIsNotATenantAdmin(t *testing.T) {
	snap := PermissionSnapshotFromRows(nil)
	ctx := scopedCtx(nil, ScopedRole{"admin", "zone", "z1"})
	assert.False(t, ctx.IsAdmin(), "IsAdmin looks at tenant-wide roles only")
	assert.True(t, snap.CanAccessRecord(ctx, "waf.rules", ruleScopes, "r", rec(map[string]any{"zoneId": "z1"}), ActionDelete, "").Allowed,
		"admin inside its scope")
	assert.False(t, snap.CanAccessRecord(ctx, "waf.rules", ruleScopes, "r", rec(map[string]any{"zoneId": "z2"}), ActionDelete, "").Allowed)
}

func TestRecordPoliciesApplyUnderTheGroupsOwnRoles(t *testing.T) {
	// analyst may read, but only rules whose status is "open" (a record-level policy gated to analyst).
	var open PolicyCondition
	require.NoError(t, json.Unmarshal([]byte(`{"attribute":"status","op":"eq","value":{"literal":"open"}}`), &open))
	rp := PolicyRow{Entity: "waf.rules", Action: "read", Roles: []string{"analyst"}, HasRoles: true, Subject: "record",
		Effect: EffectAllow, Condition: &open}
	snap := PermissionSnapshotFromRows([]PolicyRow{allow("read", "analyst"), rp})
	ctx := scopedCtx(nil, ScopedRole{"analyst", "zone", "z1"})
	assert.True(t, snap.CanAccessRecord(ctx, "waf.rules", ruleScopes, "r", rec(map[string]any{"zoneId": "z1", "status": "open"}), ActionRead, "").Allowed)
	assert.False(t, snap.CanAccessRecord(ctx, "waf.rules", ruleScopes, "r", rec(map[string]any{"zoneId": "z1", "status": "closed"}), ActionRead, "").Allowed,
		"the group's record-level policy still applies inside the scope")
}

func TestCanCreateInScope(t *testing.T) {
	snap := PermissionSnapshotFromRows([]PolicyRow{allow("create", "editor")})
	ctx := scopedCtx(nil, ScopedRole{"editor", "zone", "z1"})
	assert.True(t, snap.CanCreateInScope(ctx, "waf.rules", ruleScopes, rec(map[string]any{"zoneId": "z1"})).Allowed)
	assert.False(t, snap.CanCreateInScope(ctx, "waf.rules", ruleScopes, rec(map[string]any{"zoneId": "z2"})).Allowed, "into another zone")
	assert.False(t, snap.CanCreateInScope(ctx, "waf.rules", ruleScopes, rec(map[string]any{})).Allowed, "no scope field at all")
	assert.True(t, snap.CanCreateInScope(&RequestContext{TenantID: "t"}, "waf.rules", ruleScopes, rec(map[string]any{})).Allowed,
		"without scoped roles the ordinary path decides")
}

func TestPlanAccess(t *testing.T) {
	snap := PermissionSnapshotFromRows([]PolicyRow{allow("read", "analyst"), allow("read", "viewer")})
	_, ok := snap.PlanAccess(&RequestContext{TenantID: "t", Roles: []string{"viewer"}}, "waf.rules", ActionRead)
	assert.False(t, ok, "no scoped roles: the ordinary path")

	plan, ok := snap.PlanAccess(scopedCtx(nil, ScopedRole{"analyst", "zone", "z1"}, ScopedRole{"nobody", "zone", "z2"}), "waf.rules", ActionRead)
	assert.True(t, ok)
	assert.False(t, plan.Wide)
	assert.Len(t, plan.Groups, 1, "a scoped role that grants nothing contributes no group")
	assert.Equal(t, []ScopeRef{{"zone", "z1"}}, plan.Groups[0].Group.Scopes)

	plan, _ = snap.PlanAccess(scopedCtx([]string{"viewer"}, ScopedRole{"analyst", "zone", "z1"}), "waf.rules", ActionRead)
	assert.True(t, plan.Wide)
}
