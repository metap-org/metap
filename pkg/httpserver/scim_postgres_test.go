//go:build integration

package httpserver_test

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/metap-org/metap/pkg/auth/jwks"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/internal/admin"
	"github.com/metap-org/metap/pkg/audit"
	"github.com/metap-org/metap/pkg/auth/identity"
	"github.com/metap-org/metap/pkg/permission"
	"github.com/metap-org/metap/pkg/testkit"
)

// Roadmap 115 M12: an identity provider provisions members over SCIM with an admin service
// account's API key; the admin rules (and the audit trail) still apply.
func TestSCIMProvisionsUsers(t *testing.T) {
	pool := httpTestPool(t)
	state, privPEM := newTestAppState(t, pool, httpTestEntity())
	idCfg := admin.IdentityConfig{Shared: pool, Router: state.Router, Audit: audit.NewPostgresStore(pool), PublicURL: "http://portal.test"}
	state.Identity = &idCfg
	base := startTestServer(t, state, nil)

	tenant := uuid.New()
	adminMember, _, err := identity.AddMember(context.Background(), pool, identity.NewMember{TenantID: tenant, Email: "admin@acme.io", Password: "password-1"})
	require.NoError(t, err)
	testkit.SeedRole(t, pool, tenant, adminMember.ID, "admin")
	// The admin's own service calls, as the HTTP layer makes them once it has authenticated the caller.
	users := admin.NewUserAdminService(idCfg)
	asAdmin := admin.WithCaller(context.Background(), &permission.RequestContext{
		TenantID: tenant.String(), UserID: adminMember.ID.String(), Roles: []string{"admin"}, HasRoles: true})
	sa, err := users.CreateServiceAccount(asAdmin, &admin.CreateServiceAccountRequest{Name: "entra"})
	require.NoError(t, err)
	_, err = users.AssignRole(asAdmin, &admin.RoleRequest{Id: sa.GetId(), Role: "admin"})
	require.NoError(t, err)
	key, err := users.CreateApiKey(asAdmin, &admin.CreateApiKeyRequest{UserId: sa.GetId(), Name: "scim", ExpiresInDays: 30})
	require.NoError(t, err)

	call := func(method, path, token, body string) (int, map[string]any) {
		req, err := http.NewRequest(method, base+"/scim/v2"+path, strings.NewReader(body))
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
	bob, _, err := identity.AddMember(context.Background(), pool, identity.NewMember{TenantID: tenant, Email: "bob@acme.io", Password: "password-1"})
	require.NoError(t, err)
	testkit.SeedRole(t, pool, tenant, bob.ID, "viewer")
	bobToken, err := jwks.MintRSAJWT(privPEM, tenant, bob.ID, 3600)
	require.NoError(t, err)
	code, _ = call("GET", "/Users", bobToken, "")
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

	// The exact address is found even when more than a page of other addresses contain it: the
	// identity provider that does not find its user creates it again.
	for i := 0; i < 120; i++ {
		_, _, err := identity.AddMember(context.Background(), pool, identity.NewMember{TenantID: tenant, Email: fmt.Sprintf("a%03d.ann@acme.io", i)})
		require.NoError(t, err)
	}
	_, _, err = identity.AddMember(context.Background(), pool, identity.NewMember{TenantID: tenant, Email: "ann@acme.io"})
	require.NoError(t, err)
	code, found = call("GET", `/Users?filter=userName%20eq%20%22ann@acme.io%22`, token, "")
	assert.Equal(t, 200, code)
	assert.EqualValues(t, 1, found["totalResults"], "found beyond the first page of matches")

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
	trail, err := admin.NewAuditAdminService(idCfg).SearchAudit(asAdmin, &admin.SearchAuditRequest{ActorUserId: sa.GetId(), RecordId: id})
	require.NoError(t, err)
	assert.NotEmpty(t, trail.GetEntries())

	code, _ = call("DELETE", "/Users/"+id, token, "")
	assert.Equal(t, 204, code)
	code, _ = call("GET", "/Users/"+id, token, "")
	assert.Equal(t, 404, code)
}
