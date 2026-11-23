//go:build integration

package httpserver_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/crud"
	"github.com/metap-org/metap/pkg/metadata"
	"github.com/metap-org/metap/pkg/peripherals"
)

// Declared actions over REST (roadmap 116): record- and entity-scoped, only what the entity
// declares, the handler's ServiceError in the standard envelope.
func TestDeclaredActionsOverREST(t *testing.T) {
	pool := httpTestPool(t)
	ctx := context.Background()
	tenantID, userID := uuid.New(), uuid.New()
	_, err := pool.Exec(ctx, "INSERT INTO metadata.user_roles (tenant_id, user_id, role) VALUES ($1, $2, 'admin')", tenantID, userID)
	require.NoError(t, err)
	t.Cleanup(func() {
		pool.Exec(context.Background(), "DELETE FROM metadata.user_roles WHERE tenant_id = $1", tenantID)
	})

	entity := httpTestEntity()
	entity.Actions = []metadata.EntityAction{
		{Name: "ping", Scope: metadata.ActionScopeRecord},
		{Name: "sweep", Scope: metadata.ActionScopeEntity},
	}
	state, privPEM := newTestAppState(t, pool, entity)
	reg := crud.NewActionRegistry()
	require.NoError(t, reg.Register("test.orders", "ping", func(_ context.Context, call crud.ActionCall) (crud.JsonObject, error) {
		if string(call.Input["fail"]) == "true" {
			return nil, crud.NewServiceErrorMsg(http.StatusConflict, "not_ready", "Not ready yet.")
		}
		id, _ := json.Marshal(call.ID.String())
		return crud.JsonObject{"pinged": id}, nil
	}))
	require.NoError(t, reg.Register("test.orders", "sweep", func(_ context.Context, call crud.ActionCall) (crud.JsonObject, error) {
		return crud.JsonObject{"swept": json.RawMessage("3")}, nil
	}))
	state.Actions = reg
	token, err := peripherals.MintJWT(privPEM, tenantID, userID, 3600)
	require.NoError(t, err)
	base := startTestServer(t, state, nil)

	id := uuid.NewString()
	res := doRequest(t, "POST", base+"/api/test.orders/"+id+"/actions/ping", token, nil)
	require.Equal(t, 200, res.StatusCode)
	assert.Equal(t, id, decodeJSON(t, res)["data"].(map[string]any)["pinged"])

	res = doRequest(t, "POST", base+"/api/test.orders/actions/sweep", token, map[string]any{"input": map[string]any{}})
	require.Equal(t, 200, res.StatusCode)
	assert.EqualValues(t, 3, decodeJSON(t, res)["data"].(map[string]any)["swept"])

	res = doRequest(t, "POST", base+"/api/test.orders/"+id+"/actions/ping", token, map[string]any{"input": map[string]any{"fail": true}})
	require.Equal(t, 409, res.StatusCode)
	assert.Equal(t, "not_ready", decodeJSON(t, res)["error"].(map[string]any)["code"])

	for _, path := range []string{"/api/test.orders/actions/ping", "/api/test.orders/" + id + "/actions/sweep", "/api/test.orders/actions/nope"} {
		res = doRequest(t, "POST", base+path, token, nil)
		assert.Equal(t, 404, res.StatusCode, fmt.Sprintf("%s: wrong scope or undeclared", path))
		assert.Equal(t, "action_not_found", decodeJSON(t, res)["error"].(map[string]any)["code"])
	}
	assert.Equal(t, 401, doRequest(t, "POST", base+"/api/test.orders/actions/sweep", "", nil).StatusCode)
}

// An action that declares Requires is refused before its handler runs unless the caller holds that
// permission on the entity — the handler of an action that touches no record has no other gate.
func TestDeclaredActionRequiresEntityPermission(t *testing.T) {
	pool := httpTestPool(t)
	ctx := context.Background()
	tenantID, adminID, viewerID := uuid.New(), uuid.New(), uuid.New()
	for user, role := range map[uuid.UUID]string{adminID: "admin", viewerID: "viewer"} {
		_, err := pool.Exec(ctx, "INSERT INTO metadata.user_roles (tenant_id, user_id, role) VALUES ($1, $2, $3)", tenantID, user, role)
		require.NoError(t, err)
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), "DELETE FROM metadata.user_roles WHERE tenant_id = $1", tenantID)
	})

	entity := httpTestEntity()
	entity.Actions = []metadata.EntityAction{{Name: "probe", Scope: metadata.ActionScopeEntity, Requires: "create"}}
	state, privPEM := newTestAppState(t, pool, entity)
	calls := 0
	reg := crud.NewActionRegistry()
	require.NoError(t, reg.Register("test.orders", "probe", func(context.Context, crud.ActionCall) (crud.JsonObject, error) {
		calls++
		return crud.JsonObject{"ok": json.RawMessage("true")}, nil
	}))
	state.Actions = reg
	base := startTestServer(t, state, nil)

	viewer, err := peripherals.MintJWT(privPEM, tenantID, viewerID, 3600)
	require.NoError(t, err)
	res := doRequest(t, "POST", base+"/api/test.orders/actions/probe", viewer, nil)
	assert.Equal(t, 403, res.StatusCode)
	assert.Zero(t, calls, "the handler must not run for a caller without the permission")

	admin, err := peripherals.MintJWT(privPEM, tenantID, adminID, 3600)
	require.NoError(t, err)
	res = doRequest(t, "POST", base+"/api/test.orders/actions/probe", admin, nil)
	assert.Equal(t, 200, res.StatusCode)
	assert.Equal(t, 1, calls)
}
