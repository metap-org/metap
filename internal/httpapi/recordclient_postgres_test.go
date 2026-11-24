//go:build integration

package httpapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/metap-org/metap/pkg/auth/jwks"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/crud"
	"github.com/metap-org/metap/pkg/kit/httpx"
	"github.com/metap-org/metap/pkg/metadata"
	"github.com/metap-org/metap/pkg/permission"
	"github.com/metap-org/metap/pkg/query"
	"github.com/metap-org/metap/pkg/recordclient"
)

// recordclient (the RecordBackend over REST that replaces GrpcBackend) against a real server:
// every RecordBackend method and an action, the caller's token forwarded, errors as ServiceErrors.
func TestRecordClientOverREST(t *testing.T) {
	pool := httpTestPool(t)
	ctx := context.Background()
	tenantID, userID := uuid.New(), uuid.New()
	_, err := pool.Exec(ctx, "INSERT INTO metadata.user_roles (tenant_id, user_id, role) VALUES ($1, $2, 'admin')", tenantID, userID)
	require.NoError(t, err)
	t.Cleanup(func() {
		pool.Exec(context.Background(), "DELETE FROM metadata.outbox_events WHERE aggregate_type = 'test.orders'")
		pool.Exec(context.Background(), "DELETE FROM metadata.workflow_events WHERE tenant_id = $1", tenantID)
		pool.Exec(context.Background(), fmt.Sprintf("DELETE FROM %s WHERE tenant_id = $1", testOrdersTable), tenantID)
		pool.Exec(context.Background(), "DELETE FROM metadata.user_roles WHERE tenant_id = $1", tenantID)
	})
	ensureTestOrdersTable(t, pool)
	entity := httpTestEntity()
	entity.Actions = []metadata.EntityAction{{Name: "ping", Scope: metadata.ActionScopeRecord}}
	state, privPEM := newTestAppState(t, pool, entity)
	reg := crud.NewActionRegistry()
	require.NoError(t, reg.Register("test.orders", "ping", func(_ context.Context, call crud.ActionCall) (crud.JsonObject, error) {
		return crud.JsonObject{"ok": json.RawMessage("true")}, nil
	}))
	state.Actions = reg
	base := startTestServer(t, state, nil)
	token, err := jwks.MintRSAJWT(privPEM, tenantID, userID, 3600)
	require.NoError(t, err)

	rc := &permission.RequestContext{TenantID: tenantID.String(), UserID: userID.String(), ForwardedBearerToken: token}
	client := recordclient.New(base, httpx.NewStaticServiceTokenSource(""))

	created, err := client.Create(ctx, "test.orders", crud.JsonObject{"name": json.RawMessage(`"First"`)}, rc, nil)
	require.NoError(t, err)
	require.NotEqual(t, uuid.Nil, created.ID)

	got, err := client.Get(ctx, "test.orders", created.ID, rc)
	require.NoError(t, err)
	assert.JSONEq(t, `"First"`, string(got.Record.Data["name"]))
	assert.True(t, got.Capabilities.CanUpdate)

	many, err := client.GetMany(ctx, "test.orders", []uuid.UUID{created.ID, uuid.New()}, rc)
	require.NoError(t, err)
	require.Len(t, many, 1)

	list, err := client.List(ctx, "test.orders", query.ListInput{Limit: 10, Filters: [][2]string{{"name", "First"}}}, rc)
	require.NoError(t, err)
	assert.Len(t, list.Data, 1)
	assert.True(t, list.Capabilities.CanCreate)

	updated, err := client.Update(ctx, "test.orders", created.ID, created.Version, crud.JsonObject{"name": json.RawMessage(`"Renamed"`)}, rc, nil)
	require.NoError(t, err)

	_, err = client.Update(ctx, "test.orders", created.ID, 999, crud.JsonObject{"name": json.RawMessage(`"X"`)}, rc, nil)
	se, ok := crud.AsServiceError(err)
	require.True(t, ok, "a refusal comes back as a ServiceError")
	assert.Equal(t, uint16(409), se.Status)

	moved, err := client.Transition(ctx, "test.orders", created.ID, "activate", updated.Version, nil, rc, nil)
	require.NoError(t, err)
	assert.JSONEq(t, `"active"`, string(moved.Data["status"]))

	rows, err := client.Aggregate(ctx, "test.orders", query.AggregateSpec{}, rc)
	require.NoError(t, err)
	assert.NotEmpty(t, rows)

	out, err := client.Invoke(ctx, crud.ActionCall{Entity: "test.orders", Name: "ping", ID: created.ID, RC: rc})
	require.NoError(t, err)
	assert.JSONEq(t, `true`, string(out["ok"]))

	_, err = client.Delete(ctx, "test.orders", created.ID, moved.Version, rc, nil)
	require.NoError(t, err)

	anonymous := &permission.RequestContext{TenantID: tenantID.String()}
	_, err = client.List(ctx, "test.orders", query.ListInput{Limit: 1}, anonymous)
	se, ok = crud.AsServiceError(err)
	require.True(t, ok)
	assert.Equal(t, uint16(401), se.Status, "no caller and no service token: the service refuses")
}
