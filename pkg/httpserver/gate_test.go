package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/metadata"
	"github.com/metap-org/metap/pkg/permission"
)

// gateStore serves entity-level policies from memory and fails the test if anything else is read:
// a refused request must not touch the record store, so the gate has to decide from policies alone.
type gateStore struct {
	permission.PolicyStore
	rows []permission.PolicyRow
}

func (g gateStore) FindContextPolicies(_ context.Context, _ uuid.UUID, entity, action string) ([]permission.PolicyRow, error) {
	var out []permission.PolicyRow
	for _, r := range g.rows {
		if r.Entity == entity && r.Action == action {
			out = append(out, r)
		}
	}
	return out, nil
}

func gateState(t *testing.T, rows ...permission.PolicyRow) *AppState {
	t.Helper()
	reg := metadata.NewMetadataRegistry()
	require.NoError(t, reg.Register(metadata.EntityDefinition{
		Name: "test.orders", Label: "Order", TableName: "test.orders",
		Fields:    []metadata.EntityField{{Name: "name", Label: "Name", Kind: metadata.KindString}},
		ListViews: []metadata.EntityListView{{Name: "default", Label: "Default", Fields: []string{"name"}, MaxLimit: 50}},
	}))
	var md atomic.Pointer[metadata.MetadataRegistry]
	md.Store(reg)
	return &AppState{Metadata: &md, Permissions: permission.NewPermissionService(gateStore{rows: rows})}
}

func allow(action string, roles ...string) permission.PolicyRow {
	return permission.PolicyRow{Entity: "test.orders", Action: action, Roles: roles, HasRoles: true, Effect: permission.EffectAllow}
}

// run sends one request through the gate and reports the status and whether the handler ran.
func run(t *testing.T, mw func(http.Handler) http.Handler, rc *permission.RequestContext, entity string, extra map[string]string) (int, bool, string) {
	t.Helper()
	reached := false
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { reached = true; w.WriteHeader(http.StatusNoContent) }))
	req := httptest.NewRequest(http.MethodGet, "/api/"+entity, nil)
	req.SetPathValue("entity", entity)
	for k, v := range extra {
		req.SetPathValue(k, v)
	}
	if rc != nil {
		req = withCaller(req, rc)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code, reached, rec.Body.String()
}

func caller(roles ...string) *permission.RequestContext {
	return &permission.RequestContext{TenantID: uuid.NewString(), UserID: uuid.NewString(), Roles: roles, HasRoles: true}
}

func TestGateLetsARoleThePolicyAllowsThrough(t *testing.T) {
	state := gateState(t, allow("read", "viewer"))
	code, reached, _ := run(t, gateEntity(state, permission.ActionRead), caller("viewer"), "test.orders", nil)
	require.True(t, reached)
	require.Equal(t, http.StatusNoContent, code)
}

func TestGateRefusesARoleThePolicyDoesNotAllowBeforeTheHandler(t *testing.T) {
	state := gateState(t, allow("read", "viewer"))
	code, reached, body := run(t, gateEntity(state, permission.ActionUpdate), caller("viewer"), "test.orders", nil)
	require.False(t, reached, "the handler must not run for a refused caller")
	require.Equal(t, http.StatusForbidden, code)
	var env struct{ Error struct{ Code string } }
	require.NoError(t, json.Unmarshal([]byte(body), &env))
	require.Equal(t, "forbidden", env.Error.Code)
}

func TestGateEachVerbReadsItsOwnPolicy(t *testing.T) {
	state := gateState(t, allow("read", "viewer"), allow("create", "editor"))
	for _, c := range []struct {
		verb  permission.EntityAction
		roles []string
		want  int
	}{
		{permission.ActionRead, []string{"viewer"}, http.StatusNoContent},
		{permission.ActionCreate, []string{"viewer"}, http.StatusForbidden},
		{permission.ActionCreate, []string{"editor"}, http.StatusNoContent},
		{permission.ActionDelete, []string{"editor"}, http.StatusForbidden},
	} {
		code, _, _ := run(t, gateEntity(state, c.verb), caller(c.roles...), "test.orders", nil)
		require.Equal(t, c.want, code, "%s as %v", c.verb, c.roles)
	}
}

func TestGateAdminPassesAndNoPolicyDeniesEveryoneElse(t *testing.T) {
	state := gateState(t) // no policy at all
	code, _, _ := run(t, gateEntity(state, permission.ActionRead), caller("admin"), "test.orders", nil)
	require.Equal(t, http.StatusNoContent, code, "the admin role bypasses entity-level policy")
	code, _, _ = run(t, gateEntity(state, permission.ActionRead), caller("viewer"), "test.orders", nil)
	require.Equal(t, http.StatusForbidden, code, "default deny")
}

func TestGateAnUnknownEntityIsLeftToTheRecordService(t *testing.T) {
	state := gateState(t)
	code, reached, _ := run(t, gateEntity(state, permission.ActionRead), caller("viewer"), "no.such", nil)
	require.True(t, reached, "the record service answers 404 entity_not_found, as before")
	require.Equal(t, http.StatusNoContent, code)
}

func TestGateTransitionUsesThePolicyOfThatAction(t *testing.T) {
	state := gateState(t, allow(permission.TransitionActionPolicy("submit"), "clerk"))
	mw := gateTransition(state)
	code, _, _ := run(t, mw, caller("clerk"), "test.orders", map[string]string{"action": "submit"})
	require.Equal(t, http.StatusNoContent, code)
	code, _, _ = run(t, mw, caller("clerk"), "test.orders", map[string]string{"action": "approve"})
	require.Equal(t, http.StatusForbidden, code, "a role holding only transition:submit cannot approve")
}
