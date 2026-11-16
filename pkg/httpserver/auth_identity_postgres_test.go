//go:build integration

package httpserver_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/identity"
)

// Roadmap 113 sign-in flows: one person in two tenants picks a tenant, switches without a
// password, and changing the password kills every older session while handing back a fresh one.
func TestLoginTenantChoiceSwitchAndPasswordChange(t *testing.T) {
	pool := httpTestPool(t)
	ctx := context.Background()
	acme, beta := uuid.New(), uuid.New()
	email := uuid.NewString() + "@example.com"
	a, _, err := identity.AddMember(ctx, pool, identity.NewMember{TenantID: acme, Email: email, Password: "first-password"})
	require.NoError(t, err)
	b, _, err := identity.AddMember(ctx, pool, identity.NewMember{TenantID: beta, Email: email})
	require.NoError(t, err)
	t.Cleanup(func() {
		pool.Exec(context.Background(), "DELETE FROM metadata.users WHERE identity_id = $1", a.IdentityID)
		pool.Exec(context.Background(), "DELETE FROM metadata.identities WHERE id = $1", a.IdentityID)
	})

	state, _ := newTestAppState(t, pool, httpTestEntity())
	base := startTestServer(t, state, nil)
	login := func(body map[string]any) (*http.Response, map[string]any) {
		res := doRequest(t, "POST", base+"/auth/login", "", body)
		return res, decodeJSON(t, res)
	}

	res, body := login(map[string]any{"email": email, "password": "wrong-password"})
	require.Equal(t, 401, res.StatusCode)

	res, body = login(map[string]any{"email": email, "password": "first-password"})
	require.Equal(t, 200, res.StatusCode)
	data := body["data"].(map[string]any)
	require.Equal(t, true, data["selectTenant"], "two tenants, none used before: the person picks")
	assert.Len(t, data["tenants"], 2)
	assert.Empty(t, res.Cookies(), "no session until a tenant is chosen")

	res, body = login(map[string]any{"email": email, "password": "first-password", "tenantId": uuid.New()})
	require.Equal(t, 403, res.StatusCode, "not a member there")

	res, body = login(map[string]any{"email": email, "password": "first-password", "tenantId": beta})
	require.Equal(t, 200, res.StatusCode)
	betaToken := body["data"].(map[string]any)["token"].(string)
	me := decodeJSON(t, doRequest(t, "GET", base+"/auth/me", betaToken, nil))["data"].(map[string]any)
	assert.Equal(t, b.ID.String(), me["userId"])

	// Next time, the last tenant is the default.
	res, body = login(map[string]any{"email": email, "password": "first-password"})
	require.Equal(t, 200, res.StatusCode)
	assert.Equal(t, beta.String(), body["data"].(map[string]any)["tenantId"])

	res = doRequest(t, "POST", base+"/auth/switch-tenant", betaToken, map[string]any{"tenantId": acme})
	require.Equal(t, 200, res.StatusCode)
	acmeToken := decodeJSON(t, res)["data"].(map[string]any)["token"].(string)
	me = decodeJSON(t, doRequest(t, "GET", base+"/auth/me", acmeToken, nil))["data"].(map[string]any)
	assert.Equal(t, a.ID.String(), me["userId"])
	assert.Equal(t, acme.String(), me["tenantId"])

	res = doRequest(t, "POST", base+"/auth/password", acmeToken, map[string]any{"currentPassword": "nope", "newPassword": "second-password"})
	require.Equal(t, 400, res.StatusCode)
	res = doRequest(t, "POST", base+"/auth/password", acmeToken, map[string]any{"currentPassword": "first-password", "newPassword": "short"})
	require.Equal(t, 400, res.StatusCode)
	// iat has whole-second precision: make every token so far strictly older than the change.
	time.Sleep(1100 * time.Millisecond)
	res = doRequest(t, "POST", base+"/auth/password", acmeToken, map[string]any{"currentPassword": "first-password", "newPassword": "second-password"})
	require.Equal(t, 200, res.StatusCode)
	freshToken := decodeJSON(t, res)["data"].(map[string]any)["token"].(string)

	res = doRequest(t, "GET", base+"/auth/me", freshToken, nil)
	assert.Equal(t, 200, res.StatusCode, "the session the change handed back works")
	for _, old := range []string{betaToken, acmeToken} {
		res = doRequest(t, "GET", base+"/auth/me", old, nil)
		require.Equal(t, 401, res.StatusCode, "every older session of the person, in every tenant, is revoked")
		assert.Equal(t, "session_revoked", decodeJSON(t, res)["error"].(map[string]any)["code"])
	}

	res, _ = login(map[string]any{"email": email, "password": "first-password", "tenantId": acme})
	assert.Equal(t, 401, res.StatusCode)
	res, _ = login(map[string]any{"email": email, "password": "second-password", "tenantId": acme})
	assert.Equal(t, 200, res.StatusCode)

	// A disabled membership: its session stops working, and sign-in there is refused.
	require.NoError(t, identity.SetMemberStatus(ctx, pool, acme, a.ID, identity.StatusDisabled, time.Now()))
	res = doRequest(t, "GET", base+"/auth/me", freshToken, nil)
	require.Equal(t, 401, res.StatusCode)
	assert.Equal(t, "account_disabled", decodeJSON(t, res)["error"].(map[string]any)["code"])
	res, _ = login(map[string]any{"email": email, "password": "second-password", "tenantId": acme})
	assert.Equal(t, 403, res.StatusCode)
}
