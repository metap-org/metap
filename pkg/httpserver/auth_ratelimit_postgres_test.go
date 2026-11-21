//go:build integration

package httpserver_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The public credential routes share one stricter per-IP bucket: a guesser walking through many
// identities (so no single identity locks out) or spreading over routes still runs dry, while the
// rest of the API keeps the general limit.
func TestAuthRoutesShareAStricterPerIPLimit(t *testing.T) {
	pool := httpTestPool(t)
	state, _ := newTestAppState(t, pool, httpTestEntity())
	base := startTestServer(t, state, nil)

	// Burst of 20 across login, forgot-password and invitation lookup, a different identity each
	// time; none of them is rate limited yet.
	for i := 0; i < 20; i++ {
		var path string
		switch i % 3 {
		case 0:
			path = "/auth/login"
		case 1:
			path = "/auth/password/forgot"
		default:
			path = "/auth/invitations/lookup"
		}
		res := doRequest(t, "POST", base+path, "", map[string]any{"email": fmt.Sprintf("guess%d@acme.io", i), "password": "wrong-password"})
		assert.NotEqual(t, 429, res.StatusCode, "request %d is inside the burst", i)
		res.Body.Close()
	}

	res := doRequest(t, "POST", base+"/auth/login", "", map[string]any{"email": "guess-21@acme.io", "password": "wrong-password"})
	require.Equal(t, 429, res.StatusCode, "the 21st attempt from the same address is refused")
	assert.NotEmpty(t, res.Header.Get("Retry-After"))
	assert.Equal(t, "too_many_requests", decodeJSON(t, res)["error"].(map[string]any)["code"])

	// Routes outside the guarded set keep the general limit.
	res = doRequest(t, "GET", base+"/auth/me", "", nil)
	assert.NotEqual(t, 429, res.StatusCode)
	res.Body.Close()
}
