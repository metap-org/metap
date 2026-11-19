//go:build integration

package httpserver_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/config"
	"github.com/metap-org/metap/pkg/identity"
)

// Roadmap 115 M2: signup is off by default, validates what is typed, answers the same whatever
// the address, and only a verified link creates the tenant — with the person as its admin.
func TestSelfServiceSignup(t *testing.T) {
	pool := httpTestPool(t)
	ctx := context.Background()
	state, _ := newTestAppState(t, pool, httpTestEntity())
	base := startTestServer(t, state, nil)
	email := uuid.NewString() + "@example.com"
	t.Cleanup(func() {
		pool.Exec(context.Background(), "DELETE FROM metadata.signups WHERE lower(email) = lower($1)", email)
		pool.Exec(context.Background(), "DELETE FROM metadata.user_roles WHERE user_id IN (SELECT id FROM metadata.users WHERE lower(email) = lower($1))", email)
		pool.Exec(context.Background(), "DELETE FROM metadata.users WHERE lower(email) = lower($1)", email)
		pool.Exec(context.Background(), "DELETE FROM metadata.identities WHERE lower(email) = lower($1)", email)
		pool.Exec(context.Background(), "DELETE FROM metadata.platform_configs WHERE key = $1", config.SignupEnabled)
	})
	form := map[string]any{"email": email, "password": "long-enough-pw", "tenantName": "Acme Labs"}

	// Off by default.
	res := doRequest(t, "POST", base+"/auth/signup", "", form)
	require.Equal(t, 403, res.StatusCode)
	assert.Equal(t, "signup_disabled", decodeJSON(t, res)["error"].(map[string]any)["code"])

	require.NoError(t, state.Config.SetPlatformGlobal(ctx, config.SignupEnabled, json.RawMessage("true")))

	// Validation is reported (it tells nothing about who has an account).
	res = doRequest(t, "POST", base+"/auth/signup", "", map[string]any{"email": "nope", "password": "x", "tenantName": ""})
	require.Equal(t, 400, res.StatusCode)
	fe := decodeJSON(t, res)["error"].(map[string]any)["fieldErrors"].(map[string]any)
	assert.Contains(t, fe, "email")
	assert.Contains(t, fe, "password")
	assert.Contains(t, fe, "tenantName")

	res = doRequest(t, "POST", base+"/auth/signup", "", form)
	require.Equal(t, 202, res.StatusCode)

	// Nothing exists until the link is opened.
	var n int
	require.NoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM metadata.identities WHERE lower(email) = lower($1)", email).Scan(&n))
	assert.Zero(t, n)

	// A bad or expired token is refused.
	res = doRequest(t, "POST", base+"/auth/signup/verify", "", map[string]any{"token": "nonsense"})
	require.Equal(t, 400, res.StatusCode)
	assert.Equal(t, "signup_invalid", decodeJSON(t, res)["error"].(map[string]any)["code"])
	old, err := identity.CreateSignup(ctx, pool, email, "Old", "", "long-enough-pw", time.Now().Add(-48*time.Hour))
	require.NoError(t, err)
	res = doRequest(t, "POST", base+"/auth/signup/verify", "", map[string]any{"token": old})
	assert.Equal(t, 400, res.StatusCode)

	// The real link creates the tenant, makes the person its admin, and signs them in.
	token, err := identity.CreateSignup(ctx, pool, email, "Acme Labs", "Ada", "long-enough-pw", time.Now().Add(-2*time.Hour))
	require.NoError(t, err)
	require.NotEmpty(t, token)
	res = doRequest(t, "POST", base+"/auth/signup/verify", "", map[string]any{"token": token})
	require.Equal(t, 200, res.StatusCode)
	data := decodeJSON(t, res)["data"].(map[string]any)
	session := data["token"].(string)
	me := decodeJSON(t, doRequest(t, "GET", base+"/auth/me", session, nil))["data"].(map[string]any)
	assert.Equal(t, data["tenantId"], me["tenantId"])
	assert.Contains(t, me["roles"], "admin")
	var name, tier, status string
	require.NoError(t, pool.QueryRow(ctx, "SELECT name, tier, status FROM control.tenants WHERE id = $1", data["tenantId"]).Scan(&name, &tier, &status))
	assert.Equal(t, []string{"Acme Labs", "trial", "active"}, []string{name, tier, status})
	t.Cleanup(func() { pool.Exec(context.Background(), "DELETE FROM control.tenants WHERE id = $1", data["tenantId"]) })

	// The password chosen at signup works at the normal login.
	res = doRequest(t, "POST", base+"/auth/login", "", map[string]any{"email": email, "password": "long-enough-pw"})
	require.Equal(t, 200, res.StatusCode)

	// The link is single use.
	res = doRequest(t, "POST", base+"/auth/signup/verify", "", map[string]any{"token": token})
	assert.Equal(t, 400, res.StatusCode)

	// An address that already has an account keeps its password and gets a second tenant.
	again, err := identity.CreateSignup(ctx, pool, email, "Second Co", "", "a-different-password", time.Now().Add(-30*time.Minute))
	require.NoError(t, err)
	res = doRequest(t, "POST", base+"/auth/signup/verify", "", map[string]any{"token": again})
	require.Equal(t, 200, res.StatusCode)
	second := decodeJSON(t, res)["data"].(map[string]any)["tenantId"]
	assert.NotEqual(t, data["tenantId"], second)
	t.Cleanup(func() { pool.Exec(context.Background(), "DELETE FROM control.tenants WHERE id = $1", second) })
	res = doRequest(t, "POST", base+"/auth/login", "", map[string]any{"email": email, "password": "a-different-password"})
	assert.Equal(t, 401, res.StatusCode, "the password was not replaced")

	// Hourly limit per address: the 4th request inside the hour yields no link, silently.
	spam := uuid.NewString() + "@example.com"
	t.Cleanup(func() { pool.Exec(context.Background(), "DELETE FROM metadata.signups WHERE lower(email) = lower($1)", spam) })
	for i := 0; i < identity.MaxSignupsPerEmailHr; i++ {
		tok, err := identity.CreateSignup(ctx, pool, spam, "X", "", "long-enough-pw", time.Now())
		require.NoError(t, err)
		require.NotEmpty(t, tok)
	}
	tok, err := identity.CreateSignup(ctx, pool, spam, "X", "", "long-enough-pw", time.Now())
	require.NoError(t, err)
	assert.Empty(t, tok)
}
