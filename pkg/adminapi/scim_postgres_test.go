//go:build integration

package adminapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/peripherals"
)

// Roadmap 115 M12: an identity provider provisions members over SCIM with an admin service
// account's API key; the admin rules (and the audit trail) still apply.
func TestSCIMProvisionsUsers(t *testing.T) {
	h := newIdentityHarness(t)
	tenant := uuid.New()
	admin := h.member(tenant, "admin@acme.io", "admin")
	asAdmin := h.as(tenant, admin.ID)
	sa, err := h.client.Admin.CreateServiceAccount(asAdmin, &CreateServiceAccountRequest{Name: "entra"})
	require.NoError(t, err)
	_, err = h.client.Admin.AssignRole(asAdmin, &RoleRequest{Id: sa.GetId(), Role: "admin"})
	require.NoError(t, err)
	key, err := h.client.Admin.CreateApiKey(asAdmin, &CreateApiKeyRequest{UserId: sa.GetId(), Name: "scim", ExpiresInDays: 30})
	require.NoError(t, err)

	srv := httptest.NewServer(http.StripPrefix("/scim/v2", SCIMHandler(NewUserAdminService(h.cfg), h.scimAuth)))
	t.Cleanup(srv.Close)
	call := func(method, path, token, body string) (int, map[string]any) {
		req, err := http.NewRequest(method, srv.URL+"/scim/v2"+path, strings.NewReader(body))
		require.NoError(t, err)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		res, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		var out map[string]any
		_ = json.Unmarshal(raw, &out)
		return res.StatusCode, out
	}
	token := key.GetToken()

	code, _ := call("GET", "/Users", "", "")
	assert.Equal(t, 401, code, "no token")
	bob := h.member(tenant, "bob@acme.io", "viewer")
	code, _ = call("GET", "/Users", h.tokenForUser(tenant, bob.ID), "")
	assert.Equal(t, 403, code, "a non-admin cannot provision")

	code, cfg := call("GET", "/ServiceProviderConfig", token, "")
	assert.Equal(t, 200, code)
	assert.Equal(t, true, cfg["patch"].(map[string]any)["supported"])

	// Create (invited by email), then the IdP's "does it exist" probe.
	code, created := call("POST", "/Users", token, `{"userName":"carol@acme.io","displayName":"Carol","active":true}`)
	require.Equal(t, 201, code, "%v", created)
	id := created["id"].(string)
	assert.Equal(t, "carol@acme.io", created["userName"])
	assert.Equal(t, true, created["active"])
	code, _ = call("POST", "/Users", token, `{"userName":"carol@acme.io"}`)
	assert.Equal(t, 409, code, "already a member")
	code, found := call("GET", `/Users?filter=userName%20eq%20%22carol@acme.io%22`, token, "")
	assert.Equal(t, 200, code)
	assert.EqualValues(t, 1, found["totalResults"])
	code, found = call("GET", `/Users?filter=userName%20eq%20%22nobody@acme.io%22`, token, "")
	assert.Equal(t, 200, code)
	assert.EqualValues(t, 0, found["totalResults"])
	code, _ = call("GET", `/Users?filter=displayName%20co%20%22x%22`, token, "")
	assert.Equal(t, 400, code)

	// Deactivate (Entra sends "False" as a string), rename, reactivate.
	code, patched := call("PATCH", "/Users/"+id, token,
		`{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[{"op":"Replace","path":"active","value":"False"}]}`)
	require.Equal(t, 200, code, "%v", patched)
	assert.Equal(t, false, patched["active"])
	code, patched = call("PATCH", "/Users/"+id, token,
		`{"Operations":[{"op":"replace","value":{"displayName":"Carol B","active":true}}]}`)
	require.Equal(t, 200, code, "%v", patched)
	assert.Equal(t, "Carol B", patched["displayName"])
	assert.Equal(t, true, patched["active"])
	code, _ = call("PATCH", "/Users/"+id, token, `{"Operations":[{"op":"replace","path":"userName","value":"x@y.z"}]}`)
	assert.Equal(t, 400, code, "userName is not changeable")
	code, got := call("GET", "/Users/"+id, token, "")
	assert.Equal(t, 200, code)
	assert.Equal(t, "Carol B", got["displayName"])

	// The change is in the audit trail, attributed to the service account.
	trail, err := h.client.Audit.SearchAudit(asAdmin, &SearchAuditRequest{ActorUserId: sa.GetId(), RecordId: id})
	require.NoError(t, err)
	assert.NotEmpty(t, trail.GetEntries())

	code, _ = call("DELETE", "/Users/"+id, token, "")
	assert.Equal(t, 204, code)
	code, _ = call("GET", "/Users/"+id, token, "")
	assert.Equal(t, 404, code)
}

func (h *identityHarness) tokenForUser(tenant, user uuid.UUID) string {
	token, err := peripherals.MintJWT(h.privPEM, tenant, user, 3600)
	require.NoError(h.t, err)
	return token
}
