//go:build integration

package httpapi_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/auth/jwks"
	"github.com/metap-org/metap/pkg/crud"
	"github.com/metap-org/metap/pkg/metadata"
)

// An action's body is {"input": {...}} and nothing else. Anything beside it used to be dropped, so an
// input sent flat or under a misspelt key ran the action with no input at all.
func TestAnActionBodyWithAnythingButInputIsRefused(t *testing.T) {
	pool := httpTestPool(t)
	ctx := context.Background()
	tenantID, userID := uuid.New(), uuid.New()
	_, err := pool.Exec(ctx, "INSERT INTO metadata.user_roles (tenant_id, user_id, role) VALUES ($1, $2, 'admin')", tenantID, userID)
	require.NoError(t, err)
	t.Cleanup(func() {
		pool.Exec(context.Background(), "DELETE FROM metadata.user_roles WHERE tenant_id = $1", tenantID)
	})

	entity := httpTestEntity()
	entity.Actions = []metadata.EntityAction{{Name: "sweep", Scope: metadata.ActionScopeEntity, AnyMember: true}}
	state, privPEM := newTestAppState(t, pool, entity)
	var ran []crud.JsonObject
	reg := crud.NewActionRegistry()
	require.NoError(t, reg.Register("test.orders", "sweep", func(_ context.Context, call crud.ActionCall) (crud.JsonObject, error) {
		ran = append(ran, call.Input)
		return crud.JsonObject{}, nil
	}))
	state.Actions = reg
	token, err := jwks.MintRSAJWT(privPEM, tenantID, userID, 3600)
	require.NoError(t, err)
	url := startTestServer(t, state, nil) + "/api/test.orders/actions/sweep"

	for name, body := range map[string]map[string]any{
		"the input sent flat":    {"dryRun": true},
		"a misspelt key":         {"inputs": map[string]any{"dryRun": true}},
		"a key beside the input": {"input": map[string]any{}, "dryRun": true},
	} {
		res := doRequest(t, "POST", url, token, body)
		if assert.Equal(t, http.StatusBadRequest, res.StatusCode, name) {
			assert.Equal(t, "validation_failed", decodeJSON(t, res)["error"].(map[string]any)["code"], name)
		}
	}
	assert.Empty(t, ran, "a refused call must not reach the handler")

	// What clients send: no body, an empty object, a null or an empty input, and a real one.
	for _, body := range []map[string]any{nil, {}, {"input": nil}, {"input": map[string]any{}}, {"input": map[string]any{"dryRun": true}}} {
		assert.Equal(t, http.StatusOK, doRequest(t, "POST", url, token, body).StatusCode, "%v", body)
	}
	require.Len(t, ran, 5)
	assert.JSONEq(t, "true", string(ran[4]["dryRun"]))
}
