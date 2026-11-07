package permission

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func scopedContext(scope string) *RequestContext {
	ctx := &RequestContext{TenantID: "t1", UserID: "u1", Roles: []string{"admin"}, HasRoles: true}
	ctx.ApplyOAuthGrant(scope, "c1", true)
	return ctx
}

func TestOrdinarySessionHasNoScopeRestriction(t *testing.T) {
	ctx := &RequestContext{TenantID: "t1"}
	_, ok := ctx.OAuthScope()
	require.False(t, ok, "expected no scope")
	require.True(t, ctx.ScopePermits("waf.zones", ActionDelete), "expected permit")
	require.True(t, ctx.ScopePermitsAdminSurface(), "expected permit")
}

func TestScopeMapsVerbsToActionsAndMatchesEntitiesExactlyOrByWildcard(t *testing.T) {
	ctx := scopedContext("read:waf.zones write:waf.rules delete:*")
	require.True(t, ctx.ScopePermits("waf.zones", ActionRead), "expected permit")
	require.False(t, ctx.ScopePermits("waf.zones", ActionCreate), "expected deny")
	require.False(t, ctx.ScopePermits("waf.rules", ActionRead), "expected deny")
	require.True(t, ctx.ScopePermits("waf.rules", ActionCreate), "expected permit")
	require.True(t, ctx.ScopePermits("waf.rules", ActionUpdate), "expected permit")
	require.False(t, ctx.ScopePermits("waf.rules", ActionTransition), "expected deny")
	require.True(t, ctx.ScopePermits("anything.at_all", ActionDelete), "expected permit")
	require.False(t, ctx.ScopePermits("waf.zones.extra", ActionRead), "no prefix matching")
}

func TestEmptyOrUnrecognisedScopeGrantsNothingEvenToAnAdminUser(t *testing.T) {
	for _, scope := range []string{"", "profile email", "admin"} {
		ctx := scopedContext(scope)
		_, ok := ctx.OAuthScope()
		require.True(t, ok, "scope %q: expected OAuthScope ok", scope)
		require.False(t, ctx.ScopePermits("waf.zones", ActionRead), "scope %q: expected deny", scope)
	}
}

func TestAdminSurfacesNeedTheExplicitAdminScope(t *testing.T) {
	require.False(t, scopedContext("read:*").ScopePermitsAdminSurface(), "expected deny")
	require.False(t, scopedContext("").ScopePermitsAdminSurface(), "expected deny")
	require.True(t, scopedContext("admin").ScopePermitsAdminSurface(), "expected permit")
}

func baseContext(attrs map[string]json.RawMessage) *RequestContext {
	return &RequestContext{TenantID: "t1", UserID: "u1", Roles: []string{"employee"}, HasRoles: true, ContextAttributes: attrs}
}

func TestContextAttributesNoneProducesNoExtraKeys(t *testing.T) {
	var got map[string]any
	_ = json.Unmarshal(baseContext(nil).ToValue(), &got)
	want := map[string]any{"tenantId": "t1", "userId": "u1", "roles": []any{"employee"}}
	gotB, _ := json.Marshal(got)
	wantB, _ := json.Marshal(want)
	require.Equal(t, string(wantB), string(gotB), "got %s, want %s", gotB, wantB)
}

func TestContextAttributesAreFlattenedToTheTopLevel(t *testing.T) {
	attrs := map[string]json.RawMessage{"departmentId": rawJSON("dept-sales")}
	var got map[string]any
	_ = json.Unmarshal(baseContext(attrs).ToValue(), &got)
	require.Equal(t, "dept-sales", got["departmentId"], "got %v", got["departmentId"])
	require.Equal(t, "t1", got["tenantId"], "got %v", got["tenantId"])
}
