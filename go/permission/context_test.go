package permission

import (
	"encoding/json"
	"testing"
)

func scopedContext(scope string) *RequestContext {
	ctx := &RequestContext{TenantID: "t1", UserID: "u1", Roles: []string{"admin"}, HasRoles: true}
	ctx.ApplyOAuthGrant(scope, "c1", true)
	return ctx
}

func TestOrdinarySessionHasNoScopeRestriction(t *testing.T) {
	ctx := &RequestContext{TenantID: "t1"}
	if _, ok := ctx.OAuthScope(); ok {
		t.Fatal("expected no scope")
	}
	if !ctx.ScopePermits("waf.zones", ActionDelete) {
		t.Fatal("expected permit")
	}
	if !ctx.ScopePermitsAdminSurface() {
		t.Fatal("expected permit")
	}
}

func TestScopeMapsVerbsToActionsAndMatchesEntitiesExactlyOrByWildcard(t *testing.T) {
	ctx := scopedContext("read:waf.zones write:waf.rules delete:*")
	if !ctx.ScopePermits("waf.zones", ActionRead) {
		t.Fatal("expected permit")
	}
	if ctx.ScopePermits("waf.zones", ActionCreate) {
		t.Fatal("expected deny")
	}
	if ctx.ScopePermits("waf.rules", ActionRead) {
		t.Fatal("expected deny")
	}
	if !ctx.ScopePermits("waf.rules", ActionCreate) {
		t.Fatal("expected permit")
	}
	if !ctx.ScopePermits("waf.rules", ActionUpdate) {
		t.Fatal("expected permit")
	}
	if ctx.ScopePermits("waf.rules", ActionTransition) {
		t.Fatal("expected deny")
	}
	if !ctx.ScopePermits("anything.at_all", ActionDelete) {
		t.Fatal("expected permit")
	}
	if ctx.ScopePermits("waf.zones.extra", ActionRead) {
		t.Fatal("no prefix matching")
	}
}

func TestEmptyOrUnrecognisedScopeGrantsNothingEvenToAnAdminUser(t *testing.T) {
	for _, scope := range []string{"", "profile email", "admin"} {
		ctx := scopedContext(scope)
		if _, ok := ctx.OAuthScope(); !ok {
			t.Fatalf("scope %q: expected OAuthScope ok", scope)
		}
		if ctx.ScopePermits("waf.zones", ActionRead) {
			t.Fatalf("scope %q: expected deny", scope)
		}
	}
}

func TestAdminSurfacesNeedTheExplicitAdminScope(t *testing.T) {
	if scopedContext("read:*").ScopePermitsAdminSurface() {
		t.Fatal("expected deny")
	}
	if scopedContext("").ScopePermitsAdminSurface() {
		t.Fatal("expected deny")
	}
	if !scopedContext("admin").ScopePermitsAdminSurface() {
		t.Fatal("expected permit")
	}
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
	if string(gotB) != string(wantB) {
		t.Fatalf("got %s, want %s", gotB, wantB)
	}
}

func TestContextAttributesAreFlattenedToTheTopLevel(t *testing.T) {
	attrs := map[string]json.RawMessage{"departmentId": rawJSON("dept-sales")}
	var got map[string]any
	_ = json.Unmarshal(baseContext(attrs).ToValue(), &got)
	if got["departmentId"] != "dept-sales" {
		t.Fatalf("got %v", got["departmentId"])
	}
	if got["tenantId"] != "t1" {
		t.Fatalf("got %v", got["tenantId"])
	}
}
