//go:build integration

package httpserver_test

import (
	"context"
	"fmt"
	"github.com/metap-org/metap/pkg/auth/jwks"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// GET /api/{entity}?ids=: the records that exist and the caller may read, in one call (what the
// API gateway's cross-service expand batches into). expand/fields are list parameters, never filters.
func TestListByIDs(t *testing.T) {
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

	var ids []string
	for _, name := range []string{"A", "B", "C"} {
		res := doRequest(t, "POST", base+"/api/test.orders", token, map[string]any{"data": map[string]any{"name": name}})
		require.Equal(t, 201, res.StatusCode)
		ids = append(ids, decodeJSON(t, res)["data"].(map[string]any)["id"].(string))
	}

	res := doRequest(t, "GET", base+"/api/test.orders?ids="+ids[0]+","+ids[2]+","+uuid.NewString(), token, nil)
	require.Equal(t, 200, res.StatusCode)
	got := map[string]bool{}
	for _, r := range decodeJSON(t, res)["data"].([]any) {
		got[r.(map[string]any)["id"].(string)] = true
	}
	assert.Equal(t, map[string]bool{ids[0]: true, ids[2]: true}, got, "an unknown id is simply absent")

	res = doRequest(t, "GET", base+"/api/test.orders?ids=nope", token, nil)
	require.Equal(t, 400, res.StatusCode)
	assert.Contains(t, decodeJSON(t, res)["error"].(map[string]any)["fieldErrors"], "ids")

	res = doRequest(t, "GET", base+"/api/test.orders?expand=customerId&fields=name", token, nil)
	require.Equal(t, 200, res.StatusCode, "reserved parameters are not field filters")
	assert.Len(t, decodeJSON(t, res)["data"], 3)
}
