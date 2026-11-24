//go:build integration

package httpapi_test

import (
	"context"
	"fmt"
	"github.com/metap-org/metap/pkg/auth/jwks"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ?fields= trims each record's data to the named fields on list, ids and get; the envelope stays,
// and a name the entity does not have is a 400 that names it.
func TestFieldsProjection(t *testing.T) {
	pool := httpTestPool(t)
	ctx := context.Background()
	tenantID, userID := uuid.New(), uuid.New()
	_, err := pool.Exec(ctx, "INSERT INTO metadata.user_roles (tenant_id, user_id, role) VALUES ($1, $2, 'admin')", tenantID, userID)
	require.NoError(t, err)
	t.Cleanup(func() {
		pool.Exec(context.Background(), "DELETE FROM metadata.outbox_events WHERE aggregate_type = 'test.orders'")
		pool.Exec(context.Background(), fmt.Sprintf("DELETE FROM %s WHERE tenant_id = $1", testOrdersTable), tenantID)
		pool.Exec(context.Background(), "DELETE FROM metadata.user_roles WHERE tenant_id = $1", tenantID)
	})
	ensureTestOrdersTable(t, pool)
	state, privPEM := newTestAppState(t, pool, httpTestEntity())
	token, err := jwks.MintRSAJWT(privPEM, tenantID, userID, 3600)
	require.NoError(t, err)
	base := startTestServer(t, state, nil)

	res := doRequest(t, "POST", base+"/api/test.orders", token, map[string]any{"data": map[string]any{"name": "A", "status": "draft"}})
	require.Equal(t, 201, res.StatusCode)
	id := decodeJSON(t, res)["data"].(map[string]any)["id"].(string)

	onlyName := func(t *testing.T, rec map[string]any) {
		t.Helper()
		data := rec["data"].(map[string]any)
		assert.Equal(t, "A", data["name"])
		assert.NotContains(t, data, "status")
		assert.Equal(t, id, rec["id"], "the envelope is kept")
		assert.Contains(t, rec, "version")
	}

	res = doRequest(t, "GET", base+"/api/test.orders?fields=name", token, nil)
	require.Equal(t, 200, res.StatusCode)
	onlyName(t, decodeJSON(t, res)["data"].([]any)[0].(map[string]any))

	res = doRequest(t, "GET", base+"/api/test.orders?ids="+id+"&fields=name", token, nil)
	require.Equal(t, 200, res.StatusCode)
	onlyName(t, decodeJSON(t, res)["data"].([]any)[0].(map[string]any))

	res = doRequest(t, "GET", base+"/api/test.orders/"+id+"?fields=name", token, nil)
	require.Equal(t, 200, res.StatusCode)
	onlyName(t, decodeJSON(t, res)["data"].(map[string]any))

	res = doRequest(t, "GET", base+"/api/test.orders/"+id, token, nil)
	require.Equal(t, 200, res.StatusCode)
	assert.Contains(t, decodeJSON(t, res)["data"].(map[string]any)["data"], "status", "without fields every readable field comes back")

	res = doRequest(t, "GET", base+"/api/test.orders?fields=name,nope", token, nil)
	require.Equal(t, 400, res.StatusCode)
	errBody := decodeJSON(t, res)["error"].(map[string]any)
	assert.Equal(t, "validation_failed", errBody["code"])
	assert.Contains(t, errBody["fieldErrors"], "fields")
}
