//go:build integration

package httpserver_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/control"
	"github.com/metap-org/metap/pkg/httpserver"
	"github.com/metap-org/metap/pkg/identity"
	"github.com/metap-org/metap/pkg/tenantauth"
)

// mockIdP is a minimal OpenID Connect provider: discovery, JWKS, an /authorize that bounces the
// browser straight back with a code, and a /token that signs an id_token for whoever is "signing
// in" (the test sets email/subject).
type mockIdP struct {
	srv   *httptest.Server
	key   *rsa.PrivateKey
	mu    sync.Mutex
	nonce string
	email string
	sub   string
}

func startMockIdP(t *testing.T) *mockIdP {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	idp := &mockIdP{key: key}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"issuer": idp.srv.URL, "authorization_endpoint": idp.srv.URL + "/authorize",
			"token_endpoint": idp.srv.URL + "/token", "jwks_uri": idp.srv.URL + "/jwks", "response_types_supported": []string{"code"},
			"subject_types_supported": []string{"public"}, "id_token_signing_alg_values_supported": []string{"RS256"}})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]any{{"kty": "RSA", "use": "sig", "alg": "RS256", "kid": "k1",
			"n": base64.RawURLEncoding.EncodeToString(key.PublicKey.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.PublicKey.E)).Bytes())}}})
	})
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		idp.mu.Lock()
		idp.nonce = r.URL.Query().Get("nonce")
		idp.mu.Unlock()
		back, _ := url.Parse(r.URL.Query().Get("redirect_uri"))
		q := back.Query()
		q.Set("code", "the-code")
		q.Set("state", r.URL.Query().Get("state"))
		back.RawQuery = q.Encode()
		http.Redirect(w, r, back.String(), http.StatusFound)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		idp.mu.Lock()
		defer idp.mu.Unlock()
		now := time.Now()
		tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{"iss": idp.srv.URL, "sub": idp.sub, "aud": "client-1",
			"exp": now.Add(5 * time.Minute).Unix(), "iat": now.Unix(), "nonce": idp.nonce, "email": idp.email})
		tok.Header["kid"] = "k1"
		signed, err := tok.SignedString(key)
		if err != nil {
			w.WriteHeader(500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "a", "token_type": "Bearer", "id_token": signed, "expires_in": 300})
	})
	idp.srv = httptest.NewServer(mux)
	t.Cleanup(idp.srv.Close)
	return idp
}

func (idp *mockIdP) signIn(email, sub string) {
	idp.mu.Lock()
	idp.email, idp.sub = email, sub
	idp.mu.Unlock()
}

var noFollow = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func get(t *testing.T, rawURL string, cookies ...*http.Cookie) *http.Response {
	t.Helper()
	req, err := http.NewRequest("GET", rawURL, nil)
	require.NoError(t, err)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	res, err := noFollow.Do(req)
	require.NoError(t, err)
	return res
}

// ssoLogin walks the browser flow: start -> IdP authorize -> callback. It returns the callback response.
func ssoLogin(t *testing.T, base string, tenant uuid.UUID) (callback *http.Response, callbackURL string) {
	start := get(t, base+"/auth/sso/start?tenant="+tenant.String())
	require.Equal(t, 302, start.StatusCode)
	start.Body.Close()
	authz := get(t, start.Header.Get("Location"))
	require.Equal(t, 302, authz.StatusCode)
	authz.Body.Close()
	callbackURL = authz.Header.Get("Location")
	return get(t, callbackURL), callbackURL
}

// Roadmap 115 M7: sign in through the tenant's OIDC provider; domain restriction, first-time
// provisioning with the default role, single-use state, and enforcement with the owner's way back.
func TestSSOSignInProvisioningAndEnforcement(t *testing.T) {
	pool := httpTestPool(t)
	ctx := context.Background()
	idp := startMockIdP(t)
	tenant := uuid.New()
	state, _ := newTestAppState(t, pool, httpTestEntity())
	base := startTestServerWithPublicURL(t, state)

	store := state.Router.Secrets()
	ref := control.TenantSecretRef(tenant, "sso.clientSecret")
	require.NoError(t, store.PutSecret(ctx, ref, "shh"))
	cfg := &tenantauth.OidcConfig{IssuerURL: idp.srv.URL, ClientID: "client-1", ClientSecretRef: ref, Scopes: []string{"openid", "email"},
		EmailDomains: []string{"acme.io"}, DefaultRole: "viewer", PostLoginRedirect: "PORTAL/home"}
	cfg.PostLoginRedirect = base + "/home"
	require.NoError(t, tenantauth.SaveOidcConfig(ctx, pool, tenant, cfg, true))

	// A password member (the owner) and discovery by email domain.
	owner, _, err := identity.AddMember(ctx, pool, identity.NewMember{TenantID: tenant, Email: "owner@acme.io", Password: "Owner-Passw0rd!"})
	require.NoError(t, err)
	require.NoError(t, identity.SetOwner(ctx, pool, tenant, owner.ID))
	res := doRequest(t, "GET", base+"/auth/sso/discover?email=someone@acme.io", "", nil)
	data := decodeJSON(t, res)["data"].(map[string]any)
	assert.Equal(t, true, data["ssoAvailable"])
	assert.Equal(t, "/auth/sso/start?tenant="+tenant.String(), data["startUrl"])
	res = doRequest(t, "GET", base+"/auth/sso/discover?email=someone@other.org", "", nil)
	assert.Equal(t, false, decodeJSON(t, res)["data"].(map[string]any)["ssoAvailable"])

	// First sign-in provisions the member with the default role and starts a session.
	idp.signIn("newbie@acme.io", "sub-newbie")
	cb, _ := ssoLogin(t, base, tenant)
	if cb.StatusCode != 302 {
		b, _ := io.ReadAll(cb.Body)
		t.Fatalf("callback: %d %s", cb.StatusCode, b)
	}
	cb.Body.Close()
	assert.Equal(t, base+"/home", cb.Header.Get("Location"))
	var session *http.Cookie
	for _, c := range cb.Cookies() {
		if strings.Contains(c.Name, "session") && !strings.Contains(c.Name, "started") {
			session = c
		}
	}
	require.NotNil(t, session, "a session cookie is set")
	member, err := identity.FindExternalMember(ctx, pool, tenant, "oidc", "sub-newbie")
	require.NoError(t, err)
	require.NotNil(t, member)
	assert.Equal(t, "newbie@acme.io", member.Email)
	var roles []string
	rows, err := pool.Query(ctx, `SELECT role FROM metadata.user_roles WHERE tenant_id = $1 AND user_id = $2`, tenant, member.ID)
	require.NoError(t, err)
	for rows.Next() {
		var r string
		require.NoError(t, rows.Scan(&r))
		roles = append(roles, r)
	}
	rows.Close()
	assert.Equal(t, []string{"viewer"}, roles)

	// The same person signs in again as the same member (found by subject).
	cb, _ = ssoLogin(t, base, tenant)
	assert.Equal(t, 302, cb.StatusCode)
	cb.Body.Close()
	again, err := identity.FindExternalMember(ctx, pool, tenant, "oidc", "sub-newbie")
	require.NoError(t, err)
	assert.Equal(t, member.ID, again.ID)

	// An address outside the allowed domains is refused, and the state is single use.
	idp.signIn("intruder@evil.test", "sub-evil")
	cb, callbackURL := ssoLogin(t, base, tenant)
	assert.Equal(t, 403, cb.StatusCode)
	cb.Body.Close()
	replay := get(t, callbackURL)
	assert.Equal(t, 400, replay.StatusCode, "a used state cannot be replayed")
	replay.Body.Close()

	// An existing password member of the domain is tied to the IdP subject on first SSO sign-in.
	existing, _, err := identity.AddMember(ctx, pool, identity.NewMember{TenantID: tenant, Email: "veteran@acme.io", Password: "Veteran-Passw0rd!"})
	require.NoError(t, err)
	idp.signIn("veteran@acme.io", "sub-veteran")
	cb, _ = ssoLogin(t, base, tenant)
	assert.Equal(t, 302, cb.StatusCode)
	cb.Body.Close()
	linked, err := identity.FindExternalMember(ctx, pool, tenant, "oidc", "sub-veteran")
	require.NoError(t, err)
	require.NotNil(t, linked)
	assert.Equal(t, existing.ID, linked.ID)

	// Enforced: passwords stop working for everyone but the owner.
	cfg.Enforced = true
	require.NoError(t, tenantauth.SaveOidcConfig(ctx, pool, tenant, cfg, true))
	login := func(email, password string) *http.Response {
		return doRequest(t, "POST", base+"/auth/login", "", map[string]any{"email": email, "password": password, "tenantId": tenant.String()})
	}
	res = login("veteran@acme.io", "Veteran-Passw0rd!")
	assert.Equal(t, 403, res.StatusCode)
	assert.Equal(t, "sso_required", decodeJSON(t, res)["error"].(map[string]any)["code"])
	res = login("owner@acme.io", "Owner-Passw0rd!")
	assert.Equal(t, 200, res.StatusCode, "the owner keeps password sign-in")
	res.Body.Close()
	idp.signIn("veteran@acme.io", "sub-veteran")
	cb, _ = ssoLogin(t, base, tenant)
	assert.Equal(t, 302, cb.StatusCode, "SSO still works when enforced")
	cb.Body.Close()

	// A disabled member cannot get in through SSO either.
	require.NoError(t, identity.SetMemberStatus(ctx, pool, tenant, linked.ID, identity.StatusDisabled, time.Now()))
	cb, _ = ssoLogin(t, base, tenant)
	assert.Equal(t, 403, cb.StatusCode)
	cb.Body.Close()

	// No configuration, no SSO.
	require.NoError(t, tenantauth.DeleteOidcConfig(ctx, pool, tenant))
	nope := get(t, base+"/auth/sso/start?tenant="+tenant.String())
	assert.Equal(t, 404, nope.StatusCode)
	nope.Body.Close()
}

// startTestServerWithPublicURL serves state at a loopback address and sets that address as its
// public URL, which the SSO callback is built from.
func startTestServerWithPublicURL(t *testing.T, state *httpserver.AppState) string {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	state.PublicAppURL = "http://" + listener.Addr().String()
	handler := httpserver.BuildRouter(state, nil)
	server := &http.Server{Handler: handler}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { server.Close() })
	return state.PublicAppURL
}
