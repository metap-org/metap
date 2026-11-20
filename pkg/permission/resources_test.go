package permission

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type resourceStore struct {
	emptyStore
	rows map[string][]PolicyRow // "key action" -> rows
}

func (s resourceStore) FindContextPolicies(_ context.Context, _ uuid.UUID, key, action string) ([]PolicyRow, error) {
	return s.rows[key+" "+action], nil
}

func allowFor(roles ...string) PolicyRow {
	return PolicyRow{Roles: roles, HasRoles: true, Effect: EffectAllow, Subject: SubjectContext.String()}
}

func declareTestResource(t *testing.T) string {
	t.Helper()
	name := "test.requests_" + uuid.NewString()[:8]
	name = "test.r" + name[len("test.requests_"):]
	DeclareResource(Resource{Name: name, Description: "request data", Actions: []ResourceAction{{Name: "view"}, {Name: "approve"}}})
	return name
}

func TestCheckResourceFollowsPoliciesAndDeniesByDefault(t *testing.T) {
	res := declareTestResource(t)
	store := resourceStore{rows: map[string][]PolicyRow{ResourceKey(res) + " view": {allowFor("analyst")}}}
	s := NewPermissionService(store)
	ctx := func(roles ...string) *RequestContext {
		return &RequestContext{TenantID: uuid.NewString(), UserID: uuid.NewString(), Roles: roles, HasRoles: true}
	}

	d, err := s.CheckResource(context.Background(), ctx("analyst"), res, "view")
	require.NoError(t, err)
	assert.True(t, d.Allowed)
	d, _ = s.CheckResource(context.Background(), ctx("viewer"), res, "view")
	assert.False(t, d.Allowed, "no policy for the role: denied")
	d, _ = s.CheckResource(context.Background(), ctx("analyst"), res, "approve")
	assert.False(t, d.Allowed, "a grant on one action says nothing about another")
	d, _ = s.CheckResource(context.Background(), ctx("admin"), res, "approve")
	assert.True(t, d.Allowed, "admin bypasses policies")

	_, err = s.CheckResource(context.Background(), ctx("analyst"), "test.undeclared", "view")
	assert.Error(t, err, "an undeclared resource is a programming error, not a denial")
	_, err = s.CheckResource(context.Background(), ctx("analyst"), res, "undeclared")
	assert.Error(t, err)
}

func TestCheckResourceInAcceptsOnlyARoleHeldOnThatScope(t *testing.T) {
	res := declareTestResource(t)
	store := resourceStore{rows: map[string][]PolicyRow{ResourceKey(res) + " view": {allowFor("analyst")}}}
	s := NewPermissionService(store)
	rc := &RequestContext{TenantID: uuid.NewString(), UserID: uuid.NewString(), Roles: []string{"viewer"}, HasRoles: true,
		ScopedRoles: []ScopedRole{{Role: "analyst", ScopeType: "zone", ScopeID: "z1"}}}

	d, _ := s.CheckResourceIn(context.Background(), rc, res, "view", ScopeRef{Type: "zone", ID: "z1"})
	assert.True(t, d.Allowed)
	d, _ = s.CheckResourceIn(context.Background(), rc, res, "view", ScopeRef{Type: "zone", ID: "z2"})
	assert.False(t, d.Allowed, "a role held on zone 1 does not leak onto zone 2")
	d, _ = s.CheckResource(context.Background(), rc, res, "view")
	assert.False(t, d.Allowed, "without naming a scope only tenant-wide roles count")
}

func TestOAuthScopeMustNameTheResourceAndEvenAnAdminIsNarrowed(t *testing.T) {
	res := declareTestResource(t)
	s := NewPermissionService(resourceStore{})
	admin := func(scope string) *RequestContext {
		rc := &RequestContext{TenantID: uuid.NewString(), UserID: uuid.NewString(), Roles: []string{"admin"}, HasRoles: true}
		rc.ApplyOAuthGrant(scope, "client", true)
		return rc
	}
	for scope, want := range map[string]bool{
		"read:*":                       false, // entity scopes grant no resource
		"resource:*":                   true,
		"resource:" + res + ":*":       true,
		"resource:" + res + ":view":    true,
		"resource:" + res + ":approve": false,
		"resource:test.other:view":     false,
	} {
		d, err := s.CheckResource(context.Background(), admin(scope), res, "view")
		require.NoError(t, err)
		assert.Equal(t, want, d.Allowed, scope)
	}
}

func TestDeclareResourceRejectsMalformedAndDuplicates(t *testing.T) {
	assert.Panics(t, func() { DeclareResource(Resource{Name: "Bad Name", Actions: []ResourceAction{{Name: "x"}}}) })
	assert.Panics(t, func() { DeclareResource(Resource{Name: "test.noactions"}) })
	assert.Panics(t, func() {
		DeclareResource(Resource{Name: "test.badaction", Actions: []ResourceAction{{Name: "Bad-Action"}}})
	})
	name := declareTestResource(t)
	assert.Panics(t, func() { DeclareResource(Resource{Name: name, Actions: []ResourceAction{{Name: "view"}}}) })
	assert.True(t, ValidResourceGrant(ResourceKey(name), "view"))
	assert.False(t, ValidResourceGrant(ResourceKey(name), "nope"))
	assert.False(t, ValidResourceGrant("not.a.resource", "view"))
}
