//go:build integration

package httpserver_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/identity"
	"github.com/metap-org/metap/pkg/kit/httpx"
)

// /auth/me slides the session cookie forward, but only for the caller the cookie itself
// authenticated. With an Authorization header beside the cookie (an API key, another member's
// token) the header wins, and refreshing from it would hand the browser that caller's session.
func TestMeRefreshesOnlyTheCookiesOwnSession(t *testing.T) {
	pool := httpTestPool(t)
	ctx := context.Background()
	tenant := uuid.New()
	alice, _, err := identity.AddMember(ctx, pool, identity.NewMember{TenantID: tenant, Email: uuid.NewString() + "@example.com", Password: "alice-password"})
	require.NoError(t, err)
	bob, _, err := identity.AddMember(ctx, pool, identity.NewMember{TenantID: tenant, Email: uuid.NewString() + "@example.com", Password: "bob-password"})
	require.NoError(t, err)

	state, _ := newTestAppState(t, pool, httpTestEntity())
	base := startTestServer(t, state, nil)

	res := doRequest(t, "POST", base+"/auth/login", "", map[string]any{"email": alice.Email, "password": "alice-password"})
	require.Equal(t, 200, res.StatusCode)
	aliceCookies := res.Cookies()
	require.NotEmpty(t, aliceCookies)
	res = doRequest(t, "POST", base+"/auth/login", "", map[string]any{"email": bob.Email, "password": "bob-password"})
	require.Equal(t, 200, res.StatusCode)
	bobToken := decodeJSON(t, res)["data"].(map[string]any)["token"].(string)

	me := func(bearer string) (*http.Response, map[string]any) {
		req, err := http.NewRequest("GET", base+"/auth/me", nil)
		require.NoError(t, err)
		for _, c := range aliceCookies {
			req.AddCookie(c)
		}
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		res, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		return res, decodeJSON(t, res)["data"].(map[string]any)
	}
	sessionCookieSet := func(res *http.Response) bool {
		for _, c := range res.Cookies() {
			if c.Name == httpx.SessionCookieName {
				return true
			}
		}
		return false
	}

	res, data := me(bobToken)
	assert.Equal(t, bob.ID.String(), data["userId"], "the header authenticates the call")
	assert.False(t, sessionCookieSet(res), "Bob's token must not become the browser's session")

	res, data = me("")
	assert.Equal(t, alice.ID.String(), data["userId"])
	assert.True(t, sessionCookieSet(res), "the cookie's own session still slides forward")
}
