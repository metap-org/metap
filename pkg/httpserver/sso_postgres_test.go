//go:build integration

package httpserver_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"github.com/metap-org/metap/pkg/permission/rolestore"
	"github.com/metap-org/metap/pkg/secrets"
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

	"github.com/metap-org/metap/pkg/auth/identity"
	"github.com/metap-org/metap/pkg/auth/sso"
	"github.com/metap-org/metap/pkg/httpserver"
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
	// unverified makes the next id_token say email_verified=false; groups are sent as the
	// "groups" claim when set.
	unverified bool
	groups     []string
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
		claims := jwt.MapClaims{"iss": idp.srv.URL, "sub": idp.sub, "aud": "client-1",
			"exp": now.Add(5 * time.Minute).Unix(), "iat": now.Unix(), "nonce": idp.nonce, "email": idp.email,
			"email_verified": !idp.unverified}
		if idp.groups != nil {
			claims["groups"] = idp.groups
		}
		tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
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
	idp.email, idp.sub, idp.unverified, idp.groups = email, sub, false, nil
	idp.mu.Unlock()
}

// signInUnverified is a sign-in whose address the provider does not vouch for.
func (idp *mockIdP) signInUnverified(email, sub string) {
	idp.signIn(email, sub)
	idp.mu.Lock()
	idp.unverified = true
	idp.mu.Unlock()
}

// signInWithGroups sends the "groups" claim.
func (idp *mockIdP) signInWithGroups(email, sub string, groups ...string) {
	idp.signIn(email, sub)
	idp.mu.Lock()
	idp.groups = groups
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
	// The browser that started the sign-in carries its binding cookie back to the callback.
	return get(t, callbackURL, start.Cookies()...), callbackURL
}

// A callback URL opened by a browser that did not start the sign-in is refused (login CSRF): the
// state is valid and unused, but it belongs to another browser (audit 07 #2).
func TestSSOCallbackNeedsTheBrowserThatStarted(t *testing.T) {
	idp := startMockIdP(t)
	_, base, tenant, _, _ := setupSSOTenant(t, idp, nil)
	idp.signIn("victim@acme.io", "sub-victim")

	start := get(t, base+"/auth/sso/start?tenant="+tenant.String())
	require.Equal(t, 302, start.StatusCode)
	start.Body.Close()
	require.NotEmpty(t, start.Cookies(), "the start sets a binding cookie")
	authz := get(t, start.Header.Get("Location"))
	authz.Body.Close()
	callbackURL := authz.Header.Get("Location")

	// Another browser (no cookie, or somebody else's) opens the attacker's callback URL.
	other := get(t, callbackURL)
	assert.Equal(t, 400, other.StatusCode)
	other.Body.Close()
	// The state is spent either way, so even the right browser cannot use it afterwards.
	again := get(t, callbackURL, start.Cookies()...)
	assert.Equal(t, 400, again.StatusCode)
	again.Body.Close()
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
	ref := secrets.TenantSecretRef(tenant, "sso.clientSecret")
	require.NoError(t, store.PutSecret(ctx, ref, "shh"))
	cfg := &sso.OidcConfig{IssuerURL: idp.srv.URL, ClientID: "client-1", ClientSecretRef: ref, Scopes: []string{"openid", "email"},
		EmailDomains: []string{"acme.io"}, DefaultRole: "viewer", PostLoginRedirect: "PORTAL/home"}
	cfg.PostLoginRedirect = base + "/home"
	require.NoError(t, sso.SaveOidcConfig(ctx, pool, tenant, cfg, true))
	verifyTestDomain(t, pool, tenant, "acme.io")

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
	require.NoError(t, sso.SaveOidcConfig(ctx, pool, tenant, cfg, true))
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
	require.NoError(t, sso.DeleteOidcConfig(ctx, pool, tenant))
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

// setupSSOTenant registers an OIDC config for a new tenant against idp and returns its pieces.
func setupSSOTenant(t *testing.T, idp *mockIdP, mutate func(*sso.OidcConfig)) (state *httpserver.AppState, base string, tenant uuid.UUID, cfg *sso.OidcConfig, pool interface {
	identity.DBTX
}) {
	t.Helper()
	p := httpTestPool(t)
	tenant = uuid.New()
	state, _ = newTestAppState(t, p, httpTestEntity())
	base = startTestServerWithPublicURL(t, state)
	ref := secrets.TenantSecretRef(tenant, "sso.clientSecret")
	require.NoError(t, state.Router.Secrets().PutSecret(context.Background(), ref, "shh"))
	cfg = &sso.OidcConfig{IssuerURL: idp.srv.URL, ClientID: "client-1", ClientSecretRef: ref, Scopes: []string{"openid", "email"},
		EmailDomains: []string{"acme.io"}, PostLoginRedirect: base + "/home"}
	if mutate != nil {
		mutate(cfg)
	}
	require.NoError(t, sso.SaveOidcConfig(context.Background(), p, tenant, cfg, true))
	verifyTestDomain(t, p, tenant, "acme.io")
	return state, base, tenant, cfg, p
}

// verifyTestDomain makes the tenant the verified owner of domain, as a passed DNS check would.
func verifyTestDomain(t *testing.T, pool identity.DBTX, tenant uuid.UUID, domain string) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, sso.SyncDomains(ctx, pool, tenant, []string{domain}))
	ok, err := sso.MarkDomainVerified(ctx, pool, tenant, domain, time.Now())
	require.NoError(t, err)
	require.True(t, ok)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM metadata.sso_domains WHERE tenant_id = $1`, tenant)
	})
}

func rolesOf(t *testing.T, pool identity.DBTX, tenant, member uuid.UUID) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(), `SELECT role FROM metadata.user_roles WHERE tenant_id = $1 AND user_id = $2 ORDER BY role`, tenant, member)
	require.NoError(t, err)
	defer rows.Close()
	var roles []string
	for rows.Next() {
		var r string
		require.NoError(t, rows.Scan(&r))
		roles = append(roles, r)
	}
	return roles
}

// An address the provider does not vouch for cannot be used to take over a member or to create
// one; a person already tied to the provider by subject still signs in.
func TestSSORefusesAnAddressTheProviderDidNotVerify(t *testing.T) {
	idp := startMockIdP(t)
	_, base, tenant, _, pool := setupSSOTenant(t, idp, nil)
	ctx := context.Background()

	victim, _, err := identity.AddMember(ctx, pool, identity.NewMember{TenantID: tenant, Email: "victim@acme.io", Password: "Victim-Passw0rd!"})
	require.NoError(t, err)

	// Claiming the victim's address with an unverified claim: refused, and nothing is linked.
	idp.signInUnverified("victim@acme.io", "sub-attacker")
	cb, _ := ssoLogin(t, base, tenant)
	assert.Equal(t, 403, cb.StatusCode)
	cb.Body.Close()
	linked, err := identity.FindExternalMember(ctx, pool, tenant, "oidc", "sub-attacker")
	require.NoError(t, err)
	assert.Nil(t, linked, "no link was made")

	// An unknown address with an unverified claim is not provisioned either.
	idp.signInUnverified("squatter@acme.io", "sub-squatter")
	cb, _ = ssoLogin(t, base, tenant)
	assert.Equal(t, 403, cb.StatusCode)
	cb.Body.Close()
	_, err = identity.MemberByEmail(ctx, pool, tenant, "squatter@acme.io")
	assert.ErrorIs(t, err, identity.ErrNotFound)

	// Verified, the victim's own provider account links as before.
	idp.signIn("victim@acme.io", "sub-victim")
	cb, _ = ssoLogin(t, base, tenant)
	assert.Equal(t, 302, cb.StatusCode)
	cb.Body.Close()
	member, err := identity.FindExternalMember(ctx, pool, tenant, "oidc", "sub-victim")
	require.NoError(t, err)
	require.NotNil(t, member)
	assert.Equal(t, victim.ID, member.ID)

	// Already tied by subject: an unverified claim no longer decides who they are.
	idp.signInUnverified("victim@acme.io", "sub-victim")
	cb, _ = ssoLogin(t, base, tenant)
	assert.Equal(t, 302, cb.StatusCode)
	cb.Body.Close()
}

// The provider's group claim decides the roles named in roleMap at every sign-in, and nothing else.
func TestSSOMapsGroupsToRoles(t *testing.T) {
	idp := startMockIdP(t)
	_, base, tenant, _, pool := setupSSOTenant(t, idp, func(c *sso.OidcConfig) {
		c.DefaultRole = "viewer"
		c.RoleClaim = "groups"
		c.RoleMap = map[string]string{"sec-team": "security_analyst", "ops": "operator"}
	})
	ctx := context.Background()
	signIn := func(email, sub string, groups ...string) {
		idp.signInWithGroups(email, sub, groups...)
		cb, _ := ssoLogin(t, base, tenant)
		require.Equal(t, 302, cb.StatusCode)
		cb.Body.Close()
	}

	signIn("dana@acme.io", "sub-dana", "sec-team", "unrelated")
	dana, err := identity.FindExternalMember(ctx, pool, tenant, "oidc", "sub-dana")
	require.NoError(t, err)
	assert.Equal(t, []string{"security_analyst", "viewer"}, rolesOf(t, pool, tenant, dana.ID), "default role plus the mapped one")

	// A role an admin granted by hand is not the provider's to remove.
	require.NoError(t, rolestore.AssignRole(ctx, pool, tenant, dana.ID, "custom_reader", nil))

	// The groups change: one mapped role goes, another comes; unmanaged roles stay.
	signIn("dana@acme.io", "sub-dana", "ops")
	assert.Equal(t, []string{"custom_reader", "operator", "viewer"}, rolesOf(t, pool, tenant, dana.ID))

	// No groups at all: every managed role is withdrawn.
	signIn("dana@acme.io", "sub-dana")
	assert.Equal(t, []string{"custom_reader", "viewer"}, rolesOf(t, pool, tenant, dana.ID))

	// The owner keeps a managed role even when the claim drops it: the provider cannot lock the
	// way back in.
	boss, _, err := identity.AddMember(ctx, pool, identity.NewMember{TenantID: tenant, Email: "boss@acme.io", Password: "Boss-Passw0rd!"})
	require.NoError(t, err)
	require.NoError(t, identity.SetOwner(ctx, pool, tenant, boss.ID))
	signIn("boss@acme.io", "sub-boss", "ops")
	assert.Contains(t, rolesOf(t, pool, tenant, boss.ID), "operator")
	signIn("boss@acme.io", "sub-boss")
	assert.Contains(t, rolesOf(t, pool, tenant, boss.ID), "operator", "the owner is not demoted by the provider")
}

// A tenant that signs in through its provider does not hand out a password session when an
// invitation is accepted.
func TestAcceptingAnInvitationDoesNotBypassEnforcedSSO(t *testing.T) {
	idp := startMockIdP(t)
	_, base, tenant, cfg, pool := setupSSOTenant(t, idp, nil)
	ctx := context.Background()
	owner, _, err := identity.AddMember(ctx, pool, identity.NewMember{TenantID: tenant, Email: "owner@acme.io", Password: "Owner-Passw0rd!"})
	require.NoError(t, err)
	require.NoError(t, identity.SetOwner(ctx, pool, tenant, owner.ID))
	cfg.Enforced = true
	require.NoError(t, sso.SaveOidcConfig(ctx, pool, tenant, cfg, true))

	email := uuid.NewString() + "@acme.io"
	invited, _, err := identity.AddMember(ctx, pool, identity.NewMember{TenantID: tenant, Email: email, Status: identity.StatusInvited})
	require.NoError(t, err)
	token, _, err := identity.CreateInvitation(ctx, pool, tenant, invited.ID, invited.IdentityID, true, nil, time.Now())
	require.NoError(t, err)

	res := doRequest(t, "POST", base+"/auth/invitations/accept", "", map[string]any{"token": token, "password": "a-long-enough-password"})
	assert.Equal(t, 403, res.StatusCode)
	assert.Equal(t, "sso_required", decodeJSON(t, res)["error"].(map[string]any)["code"])
	for _, c := range res.Cookies() {
		assert.False(t, strings.Contains(c.Name, "session") && c.Value != "" && c.MaxAge >= 0, "no session cookie: %s", c.Name)
	}
}

// A workspace that only names a domain is not routed to, and cannot match or create members under
// it, until it has proved it owns the domain.
func TestSSODomainMustBeVerified(t *testing.T) {
	idp := startMockIdP(t)
	state, base, tenant, _, pool := setupSSOTenant(t, idp, nil)
	ctx := context.Background()
	// setupSSOTenant verified acme.io; un-verify it to model a freshly named domain.
	_, err := pool.Exec(ctx, `UPDATE metadata.sso_domains SET verified_at = NULL WHERE tenant_id = $1`, tenant)
	require.NoError(t, err)
	_ = state

	res := doRequest(t, "GET", base+"/auth/sso/discover?email=someone@acme.io", "", nil)
	assert.Equal(t, false, decodeJSON(t, res)["data"].(map[string]any)["ssoAvailable"], "a named but unproved domain routes nowhere")

	idp.signIn("newbie@acme.io", "sub-newbie")
	cb, _ := ssoLogin(t, base, tenant)
	assert.Equal(t, 403, cb.StatusCode, "no member is created under an unproved domain")
	cb.Body.Close()
	_, err = identity.MemberByEmail(ctx, pool, tenant, "newbie@acme.io")
	assert.ErrorIs(t, err, identity.ErrNotFound)

	ok, err := sso.MarkDomainVerified(ctx, pool, tenant, "acme.io", time.Now())
	require.NoError(t, err)
	require.True(t, ok)
	res = doRequest(t, "GET", base+"/auth/sso/discover?email=someone@acme.io", "", nil)
	assert.Equal(t, true, decodeJSON(t, res)["data"].(map[string]any)["ssoAvailable"])
	cb, _ = ssoLogin(t, base, tenant)
	assert.Equal(t, 302, cb.StatusCode)
	cb.Body.Close()
}
