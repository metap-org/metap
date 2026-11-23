//go:build integration

package httpserver_test

import (
	"context"
	"github.com/metap-org/metap/pkg/jwks"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/internal/admin"
	"github.com/metap-org/metap/pkg/audit"
	"github.com/metap-org/metap/pkg/identity"
	"github.com/metap-org/metap/pkg/testkit"
)

// User and role administration over REST (roadmap 116; was gRPC): the routes, their JSON shapes
// (camelCase, lists never null), the error envelope, and that a verify-only service serves none.
func TestAdminOperationsOverREST(t *testing.T) {
	pool := httpTestPool(t)
	ctx := context.Background()
	tenant := uuid.New()
	adminMember, _, err := identity.AddMember(ctx, pool, identity.NewMember{TenantID: tenant, Email: "admin@acme.io", Password: "password-1"})
	require.NoError(t, err)
	testkit.SeedRole(t, pool, tenant, adminMember.ID, "admin")
	plain, _, err := identity.AddMember(ctx, pool, identity.NewMember{TenantID: tenant, Email: "plain@acme.io", Password: "password-1"})
	require.NoError(t, err)

	state, privPEM := newTestAppState(t, pool, httpTestEntity())
	state.Identity = &admin.IdentityConfig{Shared: pool, Router: state.Router, Audit: audit.NewPostgresStore(pool), PublicURL: "http://portal.test"}
	base := startTestServer(t, state, nil)
	asAdmin, err := jwks.MintRSAJWT(privPEM, tenant, adminMember.ID, 3600)
	require.NoError(t, err)
	asPlain, err := jwks.MintRSAJWT(privPEM, tenant, plain.ID, 3600)
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

type memCredentials struct{ source, value map[string]string }

func (m *memCredentials) Capabilities() admin.CredentialCapabilities {
	return admin.CredentialCapabilities{Database: true, External: false, ExternalKind: "env"}
}
func (m *memCredentials) Put(_ context.Context, ref, source, value string) error {
	m.source[ref], m.value[ref] = source, value
	return nil
}
func (m *memCredentials) Get(_ context.Context, ref string) (string, error) { return m.value[ref], nil }
func (m *memCredentials) Remove(_ context.Context, ref string) error {
	delete(m.source, ref)
	delete(m.value, ref)
	return nil
}

// The Credentials tab over REST, and a cron webhook linking to a credential by id: values are
// write-only, the schema is enforced, a job cannot name a credential that does not exist, and a
// credential in use cannot be removed.
func TestCredentialOperationsOverREST(t *testing.T) {
	pool := httpTestPool(t)
	ctx := context.Background()
	tenant := uuid.New()
	adminMember, _, err := identity.AddMember(ctx, pool, identity.NewMember{TenantID: tenant, Email: "admin@acme.io", Password: "password-1"})
	require.NoError(t, err)
	testkit.SeedRole(t, pool, tenant, adminMember.ID, "admin")

	state, privPEM := newTestAppState(t, pool, httpTestEntity())
	store := &memCredentials{source: map[string]string{}, value: map[string]string{}}
	state.Identity = &admin.IdentityConfig{Shared: pool, Router: state.Router, Audit: audit.NewPostgresStore(pool), Credentials: store}
	base := startTestServer(t, state, nil)
	token, err := jwks.MintRSAJWT(privPEM, tenant, adminMember.ID, 3600)
	require.NoError(t, err)

	res := doRequest(t, "GET", base+"/admin/credentials", token, nil)
	require.Equal(t, 200, res.StatusCode)
	view := decodeJSON(t, res)
	assert.Equal(t, true, view["databaseAvailable"])
	assert.Equal(t, false, view["externalAvailable"])
	assert.Empty(t, view["credentials"])

	res = doRequest(t, "POST", base+"/admin/credentials", token,
		map[string]any{"name": "CRM webhook", "kind": "authorization", "source": "database", "value": "Bearer abc"})
	require.Equal(t, 201, res.StatusCode)
	cred := decodeJSON(t, res)["credentials"].([]any)[0].(map[string]any)
	id := cred["id"].(string)
	assert.Equal(t, "CRM webhook", cred["name"])
	assert.NotContains(t, cred, "value", "the value is never sent back")
	assert.Equal(t, []any{}, cred["usedBy"])

	res = doRequest(t, "POST", base+"/admin/credentials", token, map[string]any{"name": "x", "kind": "authorization", "source": "vault-ish", "value": "x"})
	require.Equal(t, 400, res.StatusCode)
	assert.Equal(t, "validation_failed", decodeJSON(t, res)["error"].(map[string]any)["code"])
	res = doRequest(t, "POST", base+"/admin/credentials", token, map[string]any{"name": "y", "kind": "secret", "source": "external", "value": "x"})
	assert.Equal(t, 409, res.StatusCode, "a home this deployment does not offer")
	assert.Equal(t, 401, doRequest(t, "GET", base+"/admin/credentials", "", nil).StatusCode)

	res = doRequest(t, "PATCH", base+"/admin/credentials/"+id, token, map[string]any{"description": "Auth for the CRM"})
	require.Equal(t, 200, res.StatusCode)
	assert.Equal(t, "Auth for the CRM", decodeJSON(t, res)["credentials"].([]any)[0].(map[string]any)["description"])

	// A cron webhook links by id; an unknown id is refused.
	job := func(credentialID string) map[string]any {
		return map[string]any{"name": "notify", "triggerType": "schedule", "cronExpr": "0 * * * *", "timezone": "UTC",
			"targetType": "webhook", "targetConfig": map[string]any{"url": "https://example.com/hook", "credentialId": credentialID}}
	}
	res = doRequest(t, "POST", base+"/admin/cron-jobs", token, job(uuid.NewString()))
	require.Equal(t, 400, res.StatusCode, "an unknown credential is refused")
	res = doRequest(t, "POST", base+"/admin/cron-jobs", token, job(id))
	require.Equal(t, 201, res.StatusCode)
	jobID := decodeJSON(t, res)["data"].(map[string]any)["id"].(string)

	res = doRequest(t, "GET", base+"/admin/credentials", token, nil)
	used := decodeJSON(t, res)["credentials"].([]any)[0].(map[string]any)["usedBy"].([]any)
	require.Len(t, used, 1)
	assert.Equal(t, "notify", used[0].(map[string]any)["name"])
	res = doRequest(t, "DELETE", base+"/admin/credentials/"+id, token, nil)
	require.Equal(t, 409, res.StatusCode, "a credential in use is kept")
	assert.Equal(t, "record_referenced", decodeJSON(t, res)["error"].(map[string]any)["code"])

	require.Equal(t, 204, doRequest(t, "DELETE", base+"/admin/cron-jobs/"+jobID, token, nil).StatusCode)
	res = doRequest(t, "DELETE", base+"/admin/credentials/"+id, token, nil)
	require.Equal(t, 200, res.StatusCode)
	assert.Empty(t, decodeJSON(t, res)["credentials"])
}
