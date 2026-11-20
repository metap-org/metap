//go:build integration

package httpserver_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/adminapi"
	"github.com/metap-org/metap/pkg/audit"
	"github.com/metap-org/metap/pkg/identity"
	"github.com/metap-org/metap/pkg/peripherals"
	"github.com/metap-org/metap/pkg/testkit"
)

// User and role administration over REST (roadmap 116; was gRPC): the routes, their JSON shapes
// (camelCase, lists never null), the error envelope, and that a verify-only service serves none.
func TestAdminOperationsOverREST(t *testing.T) {
	pool := httpTestPool(t)
	ctx := context.Background()
	tenant := uuid.New()
	admin, _, err := identity.AddMember(ctx, pool, identity.NewMember{TenantID: tenant, Email: "admin@acme.io", Password: "password-1"})
	require.NoError(t, err)
	testkit.SeedRole(t, pool, tenant, admin.ID, "admin")
	plain, _, err := identity.AddMember(ctx, pool, identity.NewMember{TenantID: tenant, Email: "plain@acme.io", Password: "password-1"})
	require.NoError(t, err)

	state, privPEM := newTestAppState(t, pool, httpTestEntity())
	state.Identity = &adminapi.IdentityConfig{Shared: pool, Router: state.Router, Audit: audit.NewPostgresStore(pool), PublicURL: "http://portal.test"}
	base := startTestServer(t, state, nil)
	asAdmin, err := peripherals.MintJWT(privPEM, tenant, admin.ID, 3600)
	require.NoError(t, err)
	asPlain, err := peripherals.MintJWT(privPEM, tenant, plain.ID, 3600)
	require.NoError(t, err)

	res := doRequest(t, "GET", base+"/admin/users?limit=10", asAdmin, nil)
	require.Equal(t, 200, res.StatusCode)
	list := decodeJSON(t, res)
	assert.EqualValues(t, 2, list["total"])
	users := list["users"].([]any)
	require.Len(t, users, 2)
	for _, u := range users {
		assert.NotNil(t, u.(map[string]any)["scopedRoles"], "lists are [], never null")
	}

	res = doRequest(t, "POST", base+"/admin/users", asAdmin, map[string]any{"email": "new@acme.io", "displayName": "New", "password": "password-123", "roles": []string{}})
	require.Equal(t, 201, res.StatusCode)
	created := decodeJSON(t, res)["user"].(map[string]any)
	assert.Equal(t, "new@acme.io", created["email"])
	id := created["id"].(string)

	res = doRequest(t, "PATCH", base+"/admin/users/"+id, asAdmin, map[string]any{"displayName": "Renamed"})
	require.Equal(t, 200, res.StatusCode)
	assert.Equal(t, "Renamed", decodeJSON(t, res)["displayName"])

	res = doRequest(t, "POST", base+"/admin/users/"+id+"/roles", asAdmin, map[string]any{"role": "admin"})
	require.Equal(t, 200, res.StatusCode)
	assert.Contains(t, decodeJSON(t, res)["roles"], "admin")
	res = doRequest(t, "DELETE", base+"/admin/users/"+id+"/roles/admin", asAdmin, nil)
	require.Equal(t, 200, res.StatusCode)
	assert.NotContains(t, decodeJSON(t, res)["roles"], "admin")

	// Not an admin: the standard envelope.
	res = doRequest(t, "GET", base+"/admin/users", asPlain, nil)
	require.Equal(t, 403, res.StatusCode)
	assert.Equal(t, "forbidden", decodeJSON(t, res)["error"].(map[string]any)["code"])
	assert.Equal(t, 401, doRequest(t, "GET", base+"/admin/users", "", nil).StatusCode)
	res = doRequest(t, "GET", base+"/admin/users/not-a-uuid", asAdmin, nil)
	require.Equal(t, 400, res.StatusCode)
	assert.Equal(t, "validation_failed", decodeJSON(t, res)["error"].(map[string]any)["code"])

	// The caller's own account.
	res = doRequest(t, "GET", base+"/me", asPlain, nil)
	require.Equal(t, 200, res.StatusCode)
	assert.Equal(t, "plain@acme.io", decodeJSON(t, res)["email"])
	res = doRequest(t, "GET", base+"/tenant-users", asPlain, nil)
	require.Equal(t, 200, res.StatusCode)
	assert.Len(t, decodeJSON(t, res)["users"], 3)

	res = doRequest(t, "DELETE", base+"/admin/users/"+id, asAdmin, nil)
	require.Equal(t, 204, res.StatusCode)
	res.Body.Close()

	res = doRequest(t, "GET", base+"/admin/roles", asAdmin, nil)
	require.Equal(t, 200, res.StatusCode)
	assert.NotEmpty(t, decodeJSON(t, res)["roles"])

	// A verify-only service (no Identity) serves none of it.
	state.Identity = nil
	verifyOnly := startTestServer(t, state, nil)
	assert.Equal(t, 404, doRequest(t, "GET", verifyOnly+"/admin/users", asAdmin, nil).StatusCode)
}
