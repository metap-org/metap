//go:build integration

package crud

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/control"
	"github.com/metap-org/metap/pkg/metadata"
	"github.com/metap-org/metap/pkg/peripherals"
	"github.com/metap-org/metap/pkg/permission"
	"github.com/metap-org/metap/pkg/query"
)

const (
	scopedZonesTable = "entities.test_zones"
	scopedRulesTable = "entities.test_zone_rules"
)

func scopedZoneEntity() metadata.EntityDefinition {
	return metadata.EntityDefinition{
		Name: "test.zones", Label: "Zone", TableName: scopedZonesTable,
		Fields: []metadata.EntityField{
			metadata.NewField("hostname", "Hostname", metadata.KindString).Require(),
			metadata.NewField("accountId", "Account", metadata.KindString),
		},
		Scopes: []permission.ScopeBinding{metadata.ScopeSelf("zone"), metadata.Scope("account", "accountId")},
	}
}

func scopedRuleEntity() metadata.EntityDefinition {
	return metadata.EntityDefinition{
		Name: "test.zone_rules", Label: "Rule", TableName: scopedRulesTable,
		Fields: []metadata.EntityField{
			metadata.NewField("name", "Name", metadata.KindString).Require(),
			metadata.NewField("zoneId", "Zone", metadata.KindString).Require().Index(),
			metadata.NewField("groupIds", "Groups", metadata.KindJSON),
			metadata.NewField("hits", "Hits", metadata.KindNumber),
		},
		Scopes: []permission.ScopeBinding{metadata.Scope("zone", "zoneId"), metadata.ScopeIn("group", "groupIds")},
	}
}

type scopedEnv struct {
	t      *testing.T
	svc    *CrudService
	tenant uuid.UUID
	admin  *permission.RequestContext
	zones  map[string]uuid.UUID
	rules  map[string]uuid.UUID // "<zone>/<name>" -> id
}

func newScopedEnv(t *testing.T) *scopedEnv {
	pool := testPool(t)
	svc := newCrudService(pool, scopedZoneEntity(), scopedRuleEntity())
	tenant := uuid.New()
	e := &scopedEnv{t: t, svc: svc, tenant: tenant, admin: adminContext(tenant), zones: map[string]uuid.UUID{}, rules: map[string]uuid.UUID{}}
	ctx := context.Background()

	perms := testPermissions(pool)
	grant := func(entity, action, role string) {
		_, err := perms.CreatePolicy(ctx, tenant, entity, action, []string{role}, true, nil, nil, "", nil, permission.EffectAllow)
		require.NoError(t, err)
	}
	for _, ent := range []string{"test.zones", "test.zone_rules"} {
		grant(ent, "read", "viewer")
		grant(ent, "read", "analyst")
	}
	grant("test.zone_rules", "create", "analyst")
	grant("test.zone_rules", "update", "analyst")
	grant("test.zone_rules", "delete", "analyst")

	for _, z := range []string{"z1", "z2", "z3"} {
		rec, err := svc.Create(ctx, "test.zones", JsonObject{"hostname": raw(z + ".example.com"), "accountId": raw("acme")}, e.admin, nil)
		require.NoError(t, err)
		e.zones[z] = rec.ID
		for _, n := range []string{"a", "b"} {
			r, err := svc.Create(ctx, "test.zone_rules", JsonObject{"name": raw(n), "zoneId": raw(rec.ID.String()), "hits": raw(1),
				"groupIds": raw([]string{"g-" + z})}, e.admin, nil)
			require.NoError(t, err)
			e.rules[z+"/"+n] = r.ID
		}
	}
	return e
}

// user is a caller holding the given tenant-wide roles and scoped roles.
func (e *scopedEnv) user(wide []string, scoped ...permission.ScopedRole) *permission.RequestContext {
	return &permission.RequestContext{TenantID: e.tenant.String(), UserID: uuid.New().String(), Roles: wide, HasRoles: true, ScopedRoles: scoped}
}

func (e *scopedEnv) scoped(role, zone string) permission.ScopedRole {
	return permission.ScopedRole{Role: role, ScopeType: "zone", ScopeID: e.zones[zone].String()}
}

func (e *scopedEnv) listRules(rc *permission.RequestContext) []string {
	res, err := e.svc.List(context.Background(), "test.zone_rules", query.ListInput{Limit: 100, WithTotal: true}, rc)
	require.NoError(e.t, err)
	var out []string
	for _, r := range res.Data {
		var z string
		_ = json.Unmarshal(r.Data["name"], &z)
		out = append(out, z)
	}
	return out
}

func (e *scopedEnv) ruleZones(rc *permission.RequestContext) map[string]int {
	res, err := e.svc.List(context.Background(), "test.zone_rules", query.ListInput{Limit: 100}, rc)
	require.NoError(e.t, err)
	byZone := map[string]int{}
	for _, r := range res.Data {
		var z string
		_ = json.Unmarshal(r.Data["zoneId"], &z)
		for name, id := range e.zones {
			if id.String() == z {
				byZone[name]++
			}
		}
	}
	return byZone
}

func TestScopedRoleSeesOnlyItsScope(t *testing.T) {
	e := newScopedEnv(t)
	ctx := context.Background()

	// viewer on z1 alone: reads z1's rules, nothing of z2/z3.
	u := e.user(nil, e.scoped("viewer", "z1"))
	assert.Equal(t, map[string]int{"z1": 2}, e.ruleZones(u))

	// the same through Get and the zone entity itself (scope "zone" = the record's own id).
	_, err := e.svc.Get(ctx, "test.zone_rules", e.rules["z1/a"], u)
	require.NoError(t, err)
	_, err = e.svc.Get(ctx, "test.zone_rules", e.rules["z2/a"], u)
	assert.Error(t, err, "a rule of another zone")
	zones, err := e.svc.List(ctx, "test.zones", query.ListInput{Limit: 100, WithTotal: true}, u)
	require.NoError(t, err)
	require.Len(t, zones.Data, 1, "the zone itself is in its own scope")
	assert.Equal(t, e.zones["z1"], zones.Data[0].ID)
	require.NotNil(t, zones.Page.Total)
	assert.EqualValues(t, 1, *zones.Page.Total, "the total is scoped too")

	// two zones.
	u2 := e.user(nil, e.scoped("viewer", "z1"), e.scoped("viewer", "z3"))
	assert.Equal(t, map[string]int{"z1": 2, "z3": 2}, e.ruleZones(u2))

	// a scope that matches nothing yields nothing, not everything.
	none := e.user(nil, permission.ScopedRole{Role: "viewer", ScopeType: "zone", ScopeID: uuid.NewString()})
	assert.Empty(t, e.ruleZones(none))
	// a scope type the entity has no binding for reaches nothing.
	other := e.user(nil, permission.ScopedRole{Role: "viewer", ScopeType: "planet", ScopeID: "mars"})
	assert.Empty(t, e.ruleZones(other))
}

func TestScopedRoleDoesNotLeakAcrossScopes(t *testing.T) {
	e := newScopedEnv(t)
	ctx := context.Background()
	// analyst may write, but only on z1; on z2 the user is a mere viewer.
	u := e.user(nil, e.scoped("analyst", "z1"), e.scoped("viewer", "z2"))

	// reads: both zones, never z3.
	assert.Equal(t, map[string]int{"z1": 2, "z2": 2}, e.ruleZones(u))

	// update inside the analyst scope works; inside the viewer scope it must not.
	_, err := e.svc.Update(ctx, "test.zone_rules", e.rules["z1/a"], 1, JsonObject{"hits": raw(5)}, u, nil)
	require.NoError(t, err, "analyst on z1")
	_, err = e.svc.Update(ctx, "test.zone_rules", e.rules["z2/a"], 1, JsonObject{"hits": raw(5)}, u, nil)
	assert.Error(t, err, "viewer on z2 must not inherit analyst's update from z1")
	_, err = e.svc.Update(ctx, "test.zone_rules", e.rules["z3/a"], 1, JsonObject{"hits": raw(5)}, u, nil)
	assert.Error(t, err, "outside every scope")

	// delete likewise.
	_, err = e.svc.Delete(ctx, "test.zone_rules", e.rules["z2/b"], 1, u, nil)
	assert.Error(t, err)
	_, err = e.svc.Delete(ctx, "test.zone_rules", e.rules["z1/b"], 1, u, nil)
	assert.NoError(t, err)

	// capabilities tell the same story, so a UI never offers what the write would refuse.
	g1, err := e.svc.Get(ctx, "test.zone_rules", e.rules["z1/a"], u)
	require.NoError(t, err)
	g2, err := e.svc.Get(ctx, "test.zone_rules", e.rules["z2/a"], u)
	require.NoError(t, err)
	assert.True(t, g1.Capabilities.CanUpdate)
	assert.False(t, g2.Capabilities.CanUpdate)
}

func TestScopedCreateAndMove(t *testing.T) {
	e := newScopedEnv(t)
	ctx := context.Background()
	u := e.user(nil, e.scoped("analyst", "z1"))

	_, err := e.svc.Create(ctx, "test.zone_rules", JsonObject{"name": raw("new"), "zoneId": raw(e.zones["z1"].String())}, u, nil)
	require.NoError(t, err, "inside the scope")
	_, err = e.svc.Create(ctx, "test.zone_rules", JsonObject{"name": raw("new"), "zoneId": raw(e.zones["z2"].String())}, u, nil)
	assert.Error(t, err, "into another zone")

	// An update cannot move a record out of the caller's scope either.
	_, err = e.svc.Update(ctx, "test.zone_rules", e.rules["z1/a"], 1, JsonObject{"zoneId": raw(e.zones["z2"].String())}, u, nil)
	assert.Error(t, err, "moving a rule into zone z2 puts it out of the analyst's reach")
}

func TestScopedByArrayAndAccountBindings(t *testing.T) {
	e := newScopedEnv(t)
	// "group g-z2" reaches z2's rules through the list field; "account acme" reaches every zone.
	g := e.user(nil, permission.ScopedRole{Role: "viewer", ScopeType: "group", ScopeID: "g-z2"})
	assert.Equal(t, map[string]int{"z2": 2}, e.ruleZones(g))

	acct := e.user(nil, permission.ScopedRole{Role: "viewer", ScopeType: "account", ScopeID: "acme"})
	zones, err := e.svc.List(context.Background(), "test.zones", query.ListInput{Limit: 100}, acct)
	require.NoError(t, err)
	assert.Len(t, zones.Data, 3, "the account binding reaches every zone of the account")
	assert.Empty(t, e.ruleZones(acct), "but zone_rules declares no account binding, so nothing there")
}

func TestTenantWideAndScopedRolesCombine(t *testing.T) {
	e := newScopedEnv(t)
	ctx := context.Background()
	// viewer everywhere, analyst on z1 only.
	u := e.user([]string{"viewer"}, e.scoped("analyst", "z1"))
	assert.Equal(t, map[string]int{"z1": 2, "z2": 2, "z3": 2}, e.ruleZones(u))
	_, err := e.svc.Update(ctx, "test.zone_rules", e.rules["z1/a"], 1, JsonObject{"hits": raw(2)}, u, nil)
	assert.NoError(t, err)
	_, err = e.svc.Update(ctx, "test.zone_rules", e.rules["z2/a"], 1, JsonObject{"hits": raw(2)}, u, nil)
	assert.Error(t, err, "being a tenant-wide viewer gives no write anywhere")

	// A scoped admin is an admin only inside the scope.
	sa := e.user(nil, e.scoped("admin", "z2"))
	assert.False(t, sa.IsAdmin())
	assert.Equal(t, map[string]int{"z2": 2}, e.ruleZones(sa))
	_, err = e.svc.Delete(ctx, "test.zone_rules", e.rules["z2/a"], 1, sa, nil)
	assert.NoError(t, err, "admin inside its scope")
	_, err = e.svc.Delete(ctx, "test.zone_rules", e.rules["z3/a"], 1, sa, nil)
	assert.Error(t, err)
}

func TestScopedAggregate(t *testing.T) {
	e := newScopedEnv(t)
	u := e.user(nil, e.scoped("viewer", "z1"), e.scoped("viewer", "z2"))
	res, err := e.svc.Aggregate(context.Background(), "test.zone_rules", query.AggregateSpec{Metrics: []string{"count"}}, u)
	require.NoError(t, err)
	require.Len(t, res, 1)
	assert.Contains(t, string(res[0]), "4", "z1 and z2, two rules each; z3 stays out of the count")
}

func TestScopedRolesResolvedFromTheDatabase(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tenant, user := uuid.New(), uuid.New()
	require.NoError(t, peripherals.AssignRole(ctx, pool, tenant, user, "viewer", nil))
	require.NoError(t, peripherals.AssignScopedRole(ctx, pool, tenant, user, "analyst", "zone", "z-1", nil))
	require.NoError(t, peripherals.AssignScopedRole(ctx, pool, tenant, user, "analyst", "zone", "z-1", nil), "a duplicate is a no-op")
	require.NoError(t, peripherals.AssignScopedRole(ctx, pool, tenant, user, "viewer", "account", "acme", nil))
	assert.ErrorIs(t, peripherals.AssignScopedRole(ctx, pool, tenant, user, "viewer", "Bad Type", "x", nil), peripherals.ErrInvalidScope)
	assert.ErrorIs(t, peripherals.AssignScopedRole(ctx, pool, tenant, user, "viewer", "zone", "", nil), peripherals.ErrInvalidScope)

	got, err := peripherals.ScopedRolesForUser(ctx, pool, tenant, user)
	require.NoError(t, err)
	assert.Equal(t, []permission.ScopedRole{{Role: "viewer", ScopeType: "account", ScopeID: "acme"},
		{Role: "analyst", ScopeType: "zone", ScopeID: "z-1"}}, got)

	members, err := peripherals.MembersOfScope(ctx, pool, tenant, "zone", "z-1")
	require.NoError(t, err)
	assert.Equal(t, []peripherals.ScopeMember{{UserID: user, Role: "analyst"}}, members)

	// What a request is built from.
	router := testRouter(pool)
	tx, err := router.Begin(ctx, tenant)
	require.NoError(t, err)
	sr, err := peripherals.ScopedRolesForUser(ctx, tx, tenant, user)
	require.NoError(t, err)
	require.NoError(t, tx.Commit(ctx))
	assert.Len(t, sr, 2)
	_ = control.PlatformTenantID

	require.NoError(t, peripherals.RevokeScopedRole(ctx, pool, tenant, user, "analyst", "zone", "z-1"))
	got, _ = peripherals.ScopedRolesForUser(ctx, pool, tenant, user)
	assert.Len(t, got, 1)
	// Removing every role of a user takes the scoped ones with it.
	require.NoError(t, peripherals.RevokeAllRoles(ctx, pool, tenant, user))
	got, _ = peripherals.ScopedRolesForUser(ctx, pool, tenant, user)
	assert.Empty(t, got)
	_ = fmt.Sprint
}
