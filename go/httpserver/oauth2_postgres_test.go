// E2E tests for the OAuth2 authorization server routes (routes_oauth2.go) — a real
// httpserver.BuildRouter server, hit with real HTTP requests against real Postgres. Reuses the
// pool/key/entity/server helpers already defined in http_server_postgres_test.go.
package httpserver

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/metap-org/metap-go/peripherals"
)

func mustParseQueryParam(t *testing.T, rawURL, key string) string {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	v := parsed.Query().Get(key)
	if v == "" {
		t.Fatalf("expected %q in %q", key, rawURL)
	}
	return v
}

// pkceS256 computes RFC 7636 §4.2's S256 code_challenge for a code_verifier — the same algorithm
// oauthserver.VerifyPKCE checks against, used here to build a valid test fixture rather than
// re-testing VerifyPKCE's own correctness (already unit-tested in that package).
func pkceS256(codeVerifier string) string {
	digest := sha256.Sum256([]byte(codeVerifier))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

// noRedirectClient never follows a redirect — the test inspects the Location header itself,
// same as a real OAuth2 client library would rather than letting an http.Client silently chase
// it to the (fake, unreachable) third-party redirect_uri.
var noRedirectClient = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func oauthFormRequest(t *testing.T, method, url_ string, token string, form url.Values, basicUser, basicPass string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, url_, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if basicUser != "" {
		req.SetBasicAuth(basicUser, basicPass)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

var requestIDPattern = regexp.MustCompile(`requestId:\s*"([0-9a-fA-F-]+)"`)

func extractRequestID(t *testing.T, html string) string {
	t.Helper()
	m := requestIDPattern.FindStringSubmatch(html)
	if m == nil {
		t.Fatalf("could not find requestId in consent page HTML: %s", html)
	}
	return m[1]
}

func oauthSetup(t *testing.T) (*AppState, string, string, uuid.UUID, uuid.UUID) {
	pool := httpTestPool(t)
	ctx := context.Background()
	tenantID := uuid.New()
	userID := uuid.New()

	if _, err := pool.Exec(ctx, "INSERT INTO metadata.user_roles (tenant_id, user_id, role) VALUES ($1, $2, 'admin')", tenantID, userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), "DELETE FROM metadata.oauth_consents WHERE tenant_id = $1", tenantID)
		pool.Exec(context.Background(), "DELETE FROM metadata.oauth_pending_authorizations WHERE tenant_id = $1", tenantID)
		pool.Exec(context.Background(), "DELETE FROM metadata.oauth_refresh_tokens WHERE tenant_id = $1", tenantID)
		pool.Exec(context.Background(), "DELETE FROM metadata.oauth_authorization_codes WHERE tenant_id = $1", tenantID)
		pool.Exec(context.Background(), "DELETE FROM metadata.oauth_clients WHERE tenant_id = $1", tenantID)
		pool.Exec(context.Background(), "DELETE FROM users WHERE tenant_id = $1 AND id != $2", tenantID, userID)
		pool.Exec(context.Background(), "DELETE FROM metadata.user_roles WHERE tenant_id = $1", tenantID)
	})

	state, privPEM := newTestAppState(t, pool, httpTestEntity())
	token, err := peripherals.MintJWT(privPEM, tenantID, userID, 3600)
	if err != nil {
		t.Fatal(err)
	}
	base := startTestServer(t, state, nil)
	return state, base, token, tenantID, userID
}

func createTestOAuthClient(t *testing.T, base, token string, confidential bool, scopes []string) (clientID, clientSecret string) {
	t.Helper()
	res := doRequest(t, "POST", base+"/admin/oauth/clients", token, map[string]any{
		"name":           "Test Client",
		"redirectUris":   []string{"https://client.example.test/callback"},
		"allowedScopes":  scopes,
		"isConfidential": confidential,
	})
	if res.StatusCode != 201 {
		t.Fatalf("got %d: %v", res.StatusCode, decodeJSON(t, res))
	}
	data := decodeJSON(t, res)["data"].(map[string]any)
	return data["clientId"].(string), data["clientSecret"].(string)
}

func TestOAuth2AdminClientCRUD(t *testing.T) {
	_, base, token, _, _ := oauthSetup(t)

	clientID, secret := createTestOAuthClient(t, base, token, true, []string{"read:test.orders", "write:test.orders"})
	if clientID == "" || secret == "" {
		t.Fatal("expected a non-empty clientId and clientSecret")
	}

	listRes := doRequest(t, "GET", base+"/admin/oauth/clients", token, nil)
	if listRes.StatusCode != 200 {
		t.Fatalf("got %d", listRes.StatusCode)
	}
	clients := decodeJSON(t, listRes)["data"].([]any)
	found := false
	for _, c := range clients {
		m := c.(map[string]any)
		if m["clientId"] == clientID {
			found = true
			if _, leaked := m["clientSecret"]; leaked {
				t.Fatal("the client secret must never be returned again after creation")
			}
		}
	}
	if !found {
		t.Fatal("expected the created client in the list")
	}

	// revoke by internal id
	var internalID string
	for _, c := range clients {
		m := c.(map[string]any)
		if m["clientId"] == clientID {
			internalID = m["id"].(string)
		}
	}
	revokeRes := doRequest(t, "DELETE", base+"/admin/oauth/clients/"+internalID, token, nil)
	if revokeRes.StatusCode != 204 {
		t.Fatalf("got %d", revokeRes.StatusCode)
	}
	// revoking again is a clean 404, not a 500
	againRes := doRequest(t, "DELETE", base+"/admin/oauth/clients/"+internalID, token, nil)
	if againRes.StatusCode != 404 {
		t.Fatalf("got %d", againRes.StatusCode)
	}
}

func TestOAuth2DiscoveryMetadata(t *testing.T) {
	_, base, _, _, _ := oauthSetup(t)
	res, err := http.Get(base + "/.well-known/oauth-authorization-server")
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 200 {
		t.Fatalf("got %d", res.StatusCode)
	}
	body := decodeJSON(t, res)
	if body["authorization_endpoint"] != "/oauth/authorize" || body["token_endpoint"] != "/oauth/token" {
		t.Fatalf("got %v", body)
	}
	grantTypes := body["grant_types_supported"].([]any)
	if len(grantTypes) != 3 {
		t.Fatalf("got %v", grantTypes)
	}
}

// TestOAuth2FullAuthorizationCodeFlowWithPKCEAndScopeEnforcement covers: client registration,
// the consent screen (first time), the approve decision, the authorization_code grant with
// PKCE, scope enforcement on the resulting access token (read allowed, write denied), the
// "already consented" skip-the-screen path on a second authorize call, refresh token rotation,
// reuse detection revoking the whole chain, and revoke.
func TestOAuth2FullAuthorizationCodeFlowWithPKCEAndScopeEnforcement(t *testing.T) {
	_, base, userToken, _, _ := oauthSetup(t)
	clientID, clientSecret := createTestOAuthClient(t, base, userToken, true, []string{"read:test.orders", "write:test.orders"})

	redirectURI := "https://client.example.test/callback"
	codeVerifier := "a-fixed-43-char-code-verifier-for-testing-pkce12"
	// S256(codeVerifier), base64url no-pad — computed once and pinned here rather than
	// recomputed in the test, since VerifyPKCE's own correctness is already unit-tested in
	// oauthserver; this test only needs a stable, valid pair.
	codeChallenge := pkceS256(codeVerifier)

	authorizeURL := base + "/oauth/authorize?" + url.Values{
		"response_type":         {"code"},
		"client_id":             {clientID},
		"redirect_uri":          {redirectURI},
		"scope":                 {"read:test.orders"},
		"state":                 {"xyz123"},
		"code_challenge":        {codeChallenge},
		"code_challenge_method": {"S256"},
	}.Encode()

	req, err := http.NewRequest("GET", authorizeURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+userToken)
	authorizeRes, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if authorizeRes.StatusCode != 200 {
		t.Fatalf("got %d", authorizeRes.StatusCode)
	}
	htmlBytes := make([]byte, 65536)
	n, _ := authorizeRes.Body.Read(htmlBytes)
	requestID := extractRequestID(t, string(htmlBytes[:n]))

	decisionRes := doRequest(t, "POST", base+"/oauth/authorize/decision", userToken, map[string]any{
		"requestId": requestID, "approve": true,
	})
	if decisionRes.StatusCode != 200 {
		t.Fatalf("got %d", decisionRes.StatusCode)
	}
	redirectTo := decodeJSON(t, decisionRes)["redirectTo"].(string)
	redirectURL, err := url.Parse(redirectTo)
	if err != nil {
		t.Fatal(err)
	}
	if redirectURL.Query().Get("state") != "xyz123" {
		t.Fatalf("got %v", redirectURL.Query())
	}
	code := redirectURL.Query().Get("code")
	if code == "" {
		t.Fatal("expected a code in the redirect")
	}

	// the real exchange, with the correct verifier
	tokenRes := oauthFormRequest(t, "POST", base+"/oauth/token", "", url.Values{
		"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirectURI},
		"code_verifier": {codeVerifier},
	}, clientID, clientSecret)
	if tokenRes.StatusCode != 200 {
		t.Fatalf("got %d: %v", tokenRes.StatusCode, decodeJSON(t, tokenRes))
	}
	tokenBody := decodeJSON(t, tokenRes)
	accessToken := tokenBody["access_token"].(string)
	refreshToken := tokenBody["refresh_token"].(string)
	if tokenBody["token_type"] != "Bearer" || tokenBody["scope"] != "read:test.orders" {
		t.Fatalf("got %v", tokenBody)
	}

	// the same code can never be redeemed twice
	replayRes := oauthFormRequest(t, "POST", base+"/oauth/token", "", url.Values{
		"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirectURI},
		"code_verifier": {codeVerifier},
	}, clientID, clientSecret)
	if replayRes.StatusCode != 400 {
		t.Fatalf("got %d", replayRes.StatusCode)
	}

	// scope enforcement: read is allowed, write (create) is denied — the access token is
	// narrowed to read:test.orders even though the approving user is a tenant admin.
	readRes := doRequest(t, "GET", base+"/api/test.orders", accessToken, nil)
	if readRes.StatusCode != 200 {
		t.Fatalf("got %d: %v", readRes.StatusCode, decodeJSON(t, readRes))
	}
	writeRes := doRequest(t, "POST", base+"/api/test.orders", accessToken, map[string]any{"data": map[string]any{"name": "nope"}})
	if writeRes.StatusCode != 403 {
		t.Fatalf("expected the write-scoped-out token to be denied create, got %d", writeRes.StatusCode)
	}

	// a second authorize call for the same (already-consented) scope skips the screen and
	// redirects straight away.
	secondReq, _ := http.NewRequest("GET", authorizeURL, nil)
	secondReq.Header.Set("Authorization", "Bearer "+userToken)
	secondRes, err := noRedirectClient.Do(secondReq)
	if err != nil {
		t.Fatal(err)
	}
	if secondRes.StatusCode != http.StatusFound {
		t.Fatalf("expected a 302 redirect on a returning, already-consented authorize call, got %d", secondRes.StatusCode)
	}
	loc := secondRes.Header.Get("Location")
	if !strings.HasPrefix(loc, redirectURI) {
		t.Fatalf("got %q", loc)
	}
	secondCode := mustParseQueryParam(t, loc, "code")

	// exchanging a real code without code_verifier is rejected — this burns the code (a code is
	// single-use regardless of whether the PKCE check that follows passes, same as the Rust
	// original), so it uses this second, otherwise-unused code rather than the one already
	// exchanged above.
	missingVerifierRes := oauthFormRequest(t, "POST", base+"/oauth/token", "", url.Values{
		"grant_type": {"authorization_code"}, "code": {secondCode}, "redirect_uri": {redirectURI},
	}, clientID, clientSecret)
	if missingVerifierRes.StatusCode != 400 {
		t.Fatalf("got %d", missingVerifierRes.StatusCode)
	}

	// refresh rotation
	refreshRes := oauthFormRequest(t, "POST", base+"/oauth/token", "", url.Values{
		"grant_type": {"refresh_token"}, "refresh_token": {refreshToken},
	}, clientID, clientSecret)
	if refreshRes.StatusCode != 200 {
		t.Fatalf("got %d: %v", refreshRes.StatusCode, decodeJSON(t, refreshRes))
	}
	rotated := decodeJSON(t, refreshRes)
	newRefreshToken := rotated["refresh_token"].(string)
	if newRefreshToken == refreshToken {
		t.Fatal("expected the refresh token to rotate to a new value")
	}

	// reuse detection: presenting the already-rotated-away old refresh token revokes the whole
	// chain, including the token that was just issued from it.
	reuseRes := oauthFormRequest(t, "POST", base+"/oauth/token", "", url.Values{
		"grant_type": {"refresh_token"}, "refresh_token": {refreshToken},
	}, clientID, clientSecret)
	if reuseRes.StatusCode != 400 {
		t.Fatalf("got %d", reuseRes.StatusCode)
	}
	chainRevokedRes := oauthFormRequest(t, "POST", base+"/oauth/token", "", url.Values{
		"grant_type": {"refresh_token"}, "refresh_token": {newRefreshToken},
	}, clientID, clientSecret)
	if chainRevokedRes.StatusCode != 400 {
		t.Fatalf("expected the rotated-to token to also be revoked by reuse detection, got %d", chainRevokedRes.StatusCode)
	}
}

// TestOAuth2DeniedConsentAndRevoke covers denying the consent screen, and RFC 7009's revoke
// endpoint (including its "always 200, even for garbage" posture).
func TestOAuth2DeniedConsentAndRevoke(t *testing.T) {
	_, base, userToken, _, _ := oauthSetup(t)
	clientID, clientSecret := createTestOAuthClient(t, base, userToken, true, []string{"read:test.orders"})
	redirectURI := "https://client.example.test/callback"

	authorizeURL := base + "/oauth/authorize?" + url.Values{
		"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {redirectURI},
		"scope": {"read:test.orders"},
	}.Encode()
	req, _ := http.NewRequest("GET", authorizeURL, nil)
	req.Header.Set("Authorization", "Bearer "+userToken)
	authorizeRes, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	htmlBytes := make([]byte, 65536)
	n, _ := authorizeRes.Body.Read(htmlBytes)
	requestID := extractRequestID(t, string(htmlBytes[:n]))

	decisionRes := doRequest(t, "POST", base+"/oauth/authorize/decision", userToken, map[string]any{
		"requestId": requestID, "approve": false,
	})
	if decisionRes.StatusCode != 200 {
		t.Fatalf("got %d", decisionRes.StatusCode)
	}
	redirectTo := decodeJSON(t, decisionRes)["redirectTo"].(string)
	if !strings.Contains(redirectTo, "error=access_denied") {
		t.Fatalf("got %q", redirectTo)
	}

	// revoke degrades gracefully for a garbage token, per RFC 7009 §2.2
	garbageRevokeRes := oauthFormRequest(t, "POST", base+"/oauth/revoke", "", url.Values{"token": {"not-a-real-token"}}, clientID, clientSecret)
	if garbageRevokeRes.StatusCode != 200 {
		t.Fatalf("got %d", garbageRevokeRes.StatusCode)
	}

	// wrong client credentials are rejected
	badAuthRes := oauthFormRequest(t, "POST", base+"/oauth/revoke", "", url.Values{"token": {"whatever"}}, clientID, "wrong-secret")
	if badAuthRes.StatusCode != 401 {
		t.Fatalf("got %d", badAuthRes.StatusCode)
	}
}

func TestOAuth2ClientCredentialsGrant(t *testing.T) {
	_, base, userToken, _, _ := oauthSetup(t)
	clientID, clientSecret := createTestOAuthClient(t, base, userToken, true, []string{"read:test.orders"})

	tokenRes := oauthFormRequest(t, "POST", base+"/oauth/token", "", url.Values{"grant_type": {"client_credentials"}}, clientID, clientSecret)
	if tokenRes.StatusCode != 200 {
		t.Fatalf("got %d: %v", tokenRes.StatusCode, decodeJSON(t, tokenRes))
	}
	body := decodeJSON(t, tokenRes)
	if body["access_token"] == "" || body["access_token"] == nil {
		t.Fatal("expected an access_token")
	}
	if _, hasRefresh := body["refresh_token"]; hasRefresh {
		t.Fatal("client_credentials must never issue a refresh token")
	}

	// a public (non-confidential) client cannot use client_credentials
	publicClientID, publicSecret := createTestOAuthClient(t, base, userToken, false, []string{"read:test.orders"})
	publicRes := oauthFormRequest(t, "POST", base+"/oauth/token", "", url.Values{"grant_type": {"client_credentials"}}, publicClientID, publicSecret)
	if publicRes.StatusCode != 400 {
		t.Fatalf("got %d", publicRes.StatusCode)
	}
}

func TestOAuth2PublicClientRequiresPKCE(t *testing.T) {
	_, base, userToken, _, _ := oauthSetup(t)
	clientID, _ := createTestOAuthClient(t, base, userToken, false, []string{"read:test.orders"})

	authorizeURL := base + "/oauth/authorize?" + url.Values{
		"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {"https://client.example.test/callback"},
		"scope": {"read:test.orders"},
	}.Encode()
	req, _ := http.NewRequest("GET", authorizeURL, nil)
	req.Header.Set("Authorization", "Bearer "+userToken)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 400 {
		t.Fatalf("expected a public client with no code_challenge to be rejected, got %d", res.StatusCode)
	}
}
