//go:build integration

package httpapi_test

import (
	"context"
	"io"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/auth/identity"
)

// Roadmap 116 P0: the session operations as typed REST (huma) — same behavior as the gRPC/GraphQL
// ones, in the standard error envelope, described in /metadata/api.json.
func TestSessionOperationsOverREST(t *testing.T) {
	pool := httpTestPool(t)
	ctx := context.Background()
	tenant := uuid.New()
	member, _, err := identity.AddMember(ctx, pool, identity.NewMember{TenantID: tenant, Email: uuid.NewString() + "@example.com", Password: "member-password"})
	require.NoError(t, err)

	state, _ := newTestAppState(t, pool, httpTestEntity())
	base := startTestServer(t, state, nil)
	login := func() string {
		res := doRequest(t, "POST", base+"/auth/login", "", map[string]any{"email": member.Email, "password": "member-password"})
		require.Equal(t, 200, res.StatusCode)
		return decodeJSON(t, res)["data"].(map[string]any)["token"].(string)
	}
	first, second, third := login(), login(), login()

	res := doRequest(t, "GET", base+"/me/sessions", "", nil)
	require.Equal(t, 401, res.StatusCode)
	assert.Equal(t, "unauthorized", decodeJSON(t, res)["error"].(map[string]any)["code"], "the standard envelope")

	res = doRequest(t, "GET", base+"/me/sessions", first, nil)
	require.Equal(t, 200, res.StatusCode)
	sessions := decodeJSON(t, res)["sessions"].([]any)
	require.Len(t, sessions, 3)
	var current, other string
	for _, s := range sessions {
		m := s.(map[string]any)
		if m["current"] == true {
			current = m["id"].(string)
		} else if other == "" {
			other = m["id"].(string)
		}
	}
	require.NotEmpty(t, current, "the caller's own session is flagged")

	res = doRequest(t, "DELETE", base+"/me/sessions/not-a-uuid", first, nil)
	require.Equal(t, 400, res.StatusCode)
	body := decodeJSON(t, res)["error"].(map[string]any)
	assert.Equal(t, "validation_failed", body["code"])
	assert.Contains(t, body["fieldErrors"], "id", "a schema failure names the input")

	res = doRequest(t, "DELETE", base+"/me/sessions/"+uuid.NewString(), first, nil)
	require.Equal(t, 404, res.StatusCode)
	assert.Equal(t, "not_found", decodeJSON(t, res)["error"].(map[string]any)["code"])

	res = doRequest(t, "DELETE", base+"/me/sessions/"+other, first, nil)
	require.Equal(t, 204, res.StatusCode)

	res = doRequest(t, "POST", base+"/me/sessions/revoke-others", first, nil)
	require.Equal(t, 200, res.StatusCode)
	assert.EqualValues(t, 1, decodeJSON(t, res)["count"], "one other session was left")
	for _, token := range []string{second, third} {
		assert.Equal(t, 401, doRequest(t, "GET", base+"/auth/me", token, nil).StatusCode, "signed out")
	}
	assert.Equal(t, 200, doRequest(t, "GET", base+"/auth/me", first, nil).StatusCode, "this session stays")

	res, err = http.Get(base + "/metadata/api.json")
	require.NoError(t, err)
	require.Equal(t, 200, res.StatusCode)
	doc, _ := io.ReadAll(res.Body)
	assert.Contains(t, string(doc), `"/me/sessions/{id}"`)
	assert.Contains(t, string(doc), `"listMySessions"`)

	// A browser: the session cookie, and the CSRF double-submit on a state-changing call.
	res = doRequest(t, "POST", base+"/auth/login", "", map[string]any{"email": member.Email, "password": "member-password"})
	require.Equal(t, 200, res.StatusCode)
	cookies := res.Cookies()
	csrf := ""
	for _, c := range cookies {
		if c.Name == "metap_csrf" {
			csrf = c.Value
		}
	}
	require.NotEmpty(t, csrf)
	call := func(withCSRF bool) int {
		req, err := http.NewRequest("POST", base+"/me/sessions/revoke-others", nil)
		require.NoError(t, err)
		for _, c := range cookies {
			req.AddCookie(c)
		}
		if withCSRF {
			req.Header.Set("X-CSRF-Token", csrf)
		}
		res, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		return res.StatusCode
	}
	assert.Equal(t, 401, call(false), "a cookie call without the CSRF header is refused")
	assert.Equal(t, 200, call(true))
}
