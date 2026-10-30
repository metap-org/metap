// HTTP surface for the oauthserver package — metap acting as an OAuth2 Authorization Server for
// a third-party client acting on behalf of a tenant's own user. Distinct from routes_auth.go's
// (not yet ported) oauth2_login/oauth2_callback — those are the opposite direction, a tenant's
// user logging *into* metap via an external OAuth2 IdP.
//
// oauth_clients/oauth_authorization_codes/oauth_refresh_tokens are looked up against the shared
// platform pool (state.Pool), never Router.Begin(tenant) — /oauth/token and /oauth/revoke
// resolve which tenant a request belongs to *from the row a caller-supplied client_id/code/
// refresh-token resolves to*, which carries no tenant hint of its own.
//
// Deliberate simplification, not strict RFC 6749 behavior: every error from GET /oauth/authorize,
// including one raised after client_id/redirect_uri are already confirmed registered, is
// returned as a direct HTTP error response rather than a redirect carrying ?error=.... A
// conforming client still gets a clear, immediate error; it just isn't ferried back through the
// browser redirect the spec technically prescribes for that subset of failures.
//
// Real interactive consent: authorize renders a plain server-rendered HTML page the first time a
// (client, user) pair has no prior oauthserver.GetConsentScope covering the requested scope, and
// skips straight to the redirect once one does. Its approve/deny action
// (POST /oauth/authorize/decision) is a fetch() call from an inline <script>, not a plain HTML
// form post, specifically so it can carry the X-CSRF-Token header requireAuth's cookie-session
// CSRF defense already requires on every cookie-authenticated mutation.
//
// Ported from crates/metap-http/src/routes/oauth2.rs. Admin client CRUD
// (/admin/oauth/clients*) is recovered from git history (commit 3faaad4~1, before it moved to
// GraphQL) per the user's standing direction to keep REST rather than follow that later decision.
package httpserver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/metap-org/metap-go/oauthserver"
	"github.com/metap-org/metap-go/peripherals"
	"github.com/metap-org/metap-go/tenantauth"
)

// oauthAccessTokenTTLSeconds: no config key for this yet (oauthserver's own doc comment flags it
// as a follow-up alongside client_credentials) — 1 hour matches the platform's own
// pre-config-tiers session TTL default.
const oauthAccessTokenTTLSeconds = 3600

// percentEncode is RFC 3986's unreserved-set percent-encoding — net/url's QueryEscape/
// PathEscape each use a different, not-quite-matching escaping rule (QueryEscape turns a space
// into "+", not "%20"), so this is hand-rolled to match the Rust original's own manual
// implementation byte-for-byte rather than approximated.
func percentEncode(s string) string {
	var out strings.Builder
	for i := 0; i < len(s); i++ {
		b := s[i]
		switch {
		case b >= 'A' && b <= 'Z', b >= 'a' && b <= 'z', b >= '0' && b <= '9', b == '-', b == '_', b == '.', b == '~':
			out.WriteByte(b)
		default:
			fmt.Fprintf(&out, "%%%02X", b)
		}
	}
	return out.String()
}

func withQuerySep(redirectURI string) string {
	if strings.Contains(redirectURI, "?") {
		return "&"
	}
	return "?"
}

// issueAuthorizationCode builds the redirect_uri?code=...&state=... target every successful
// authorization ends on — shared by authorize's "already consented, skip the screen" path and
// authorizeDecision's "just approved" path so the two can't drift.
func issueAuthorizationCode(ctx context.Context, state *AppState, clientID, tenantID, userID uuid.UUID,
	redirectURI, scope string, codeChallenge, codeChallengeMethod, clientState *string) (string, error) {
	_, rawCode, err := oauthserver.CreateAuthorizationCode(ctx, state.Pool, oauthserver.CreateCodeInput{
		ClientID: clientID, TenantID: tenantID, UserID: userID, RedirectURI: redirectURI, Scope: scope,
		CodeChallenge: codeChallenge, CodeChallengeMethod: codeChallengeMethod,
	})
	if err != nil {
		return "", err
	}
	redirectTo := redirectURI + withQuerySep(redirectURI) + "code=" + percentEncode(rawCode)
	if clientState != nil {
		redirectTo += "&state=" + percentEncode(*clientState)
	}
	return redirectTo, nil
}

func deniedRedirect(redirectURI string, clientState *string) string {
	redirectTo := redirectURI + withQuerySep(redirectURI) + "error=access_denied"
	if clientState != nil {
		redirectTo += "&state=" + percentEncode(*clientState)
	}
	return redirectTo
}

// consentPageHTML is a minimal, dependency-free consent screen — server-rendered here rather
// than handed off to a tenant's own frontend, since the consent decision is part of the auth
// boundary itself, not a business screen. clientName/scope are escaped (html.EscapeString, the
// stdlib equivalent of the Rust original's hand-rolled escape_html) since both ultimately come
// from a tenant admin's own free-text input.
func consentPageHTML(clientName, scope string, requestID uuid.UUID) string {
	tokens := oauthserver.ScopeTokens(scope)
	var scopeList strings.Builder
	if len(tokens) == 0 {
		scopeList.WriteString("<li>(no specific permissions requested)</li>")
	} else {
		for _, t := range tokens {
			scopeList.WriteString("<li>" + html.EscapeString(t) + "</li>")
		}
	}
	return fmt.Sprintf(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>Authorize application</title>
<style>
  body { font-family: system-ui, sans-serif; max-width: 32rem; margin: 4rem auto; padding: 0 1rem; color: #1a1a1a; }
  h1 { font-size: 1.25rem; }
  ul { background: #f5f5f5; border-radius: 0.5rem; padding: 1rem 1rem 1rem 2rem; }
  .actions { margin-top: 1.5rem; display: flex; gap: 0.75rem; }
  button { font-size: 1rem; padding: 0.5rem 1.25rem; border-radius: 0.375rem; border: 1px solid #ccc; cursor: pointer; }
  #approve { background: #111827; color: #fff; border-color: #111827; }
  #error { color: #b91c1c; margin-top: 1rem; }
</style>
</head>
<body>
  <h1>%s is requesting access to your account</h1>
  <p>This application would like to:</p>
  <ul>%s</ul>
  <div class="actions">
    <button id="approve" type="button">Approve</button>
    <button id="deny" type="button">Deny</button>
  </div>
  <p id="error" hidden></p>
  <script>
    function csrfToken() {
      const m = document.cookie.match(/(?:^|; )metap_csrf=([^;]*)/);
      return m ? decodeURIComponent(m[1]) : "";
    }
    async function decide(approve) {
      try {
        const res = await fetch("/oauth/authorize/decision", {
          method: "POST",
          credentials: "include",
          headers: { "content-type": "application/json", "x-csrf-token": csrfToken() },
          body: JSON.stringify({ requestId: "%s", approve: approve }),
        });
        const body = await res.json();
        if (res.ok && body.redirectTo) {
          window.location.href = body.redirectTo;
          return;
        }
        throw new Error((body.error && body.error.message) || "request failed");
      } catch (e) {
        const el = document.getElementById("error");
        el.hidden = false;
        el.textContent = "Something went wrong: " + e.message;
      }
    }
    document.getElementById("approve").addEventListener("click", () => decide(true));
    document.getElementById("deny").addEventListener("click", () => decide(false));
  </script>
</body>
</html>`, html.EscapeString(clientName), scopeList.String(), requestID)
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func authorizeOAuth2(w http.ResponseWriter, r *http.Request, state *AppState) {
	rc, ok := requireAuth(w, r, state)
	if !ok {
		return
	}
	q := r.URL.Query()
	if q.Get("response_type") != "code" {
		writeServiceError(w, http.StatusBadRequest, "unsupported_response_type", "Only response_type=code is supported.", nil)
		return
	}
	tenantID, err := state.Permissions.ScopedTenant(rc)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	userID, err := uuid.Parse(rc.UserID)
	if err != nil {
		writeServiceError(w, http.StatusUnauthorized, "unauthorized", "A user session is required to authorize a client.", nil)
		return
	}
	clientID := q.Get("client_id")
	redirectURI := q.Get("redirect_uri")
	scope := q.Get("scope")
	clientState := nilIfEmpty(q.Get("state"))
	codeChallenge := nilIfEmpty(q.Get("code_challenge"))
	codeChallengeMethod := nilIfEmpty(q.Get("code_challenge_method"))

	withSecret, err := oauthserver.GetClientByClientID(r.Context(), state.Pool, clientID)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	if withSecret == nil {
		writeServiceError(w, http.StatusBadRequest, "invalid_client", "Unknown client_id.", nil)
		return
	}
	client := withSecret.Client
	if client.RevokedAt != nil {
		writeServiceError(w, http.StatusBadRequest, "invalid_client", "This client has been revoked.", nil)
		return
	}
	if client.TenantID != tenantID {
		writeServiceError(w, http.StatusForbidden, "invalid_client", "This client does not belong to your tenant.", nil)
		return
	}
	registered := false
	for _, u := range client.RedirectURIs {
		if u == redirectURI {
			registered = true
			break
		}
	}
	if !registered {
		writeServiceError(w, http.StatusBadRequest, "invalid_request", "redirect_uri is not registered for this client.", nil)
		return
	}
	if codeChallengeMethod != nil && *codeChallengeMethod != "S256" {
		writeServiceError(w, http.StatusBadRequest, "invalid_request", "Only code_challenge_method=S256 is supported.", nil)
		return
	}
	if !client.IsConfidential && codeChallenge == nil {
		writeServiceError(w, http.StatusBadRequest, "invalid_request", "code_challenge (PKCE) is required for a public client.", nil)
		return
	}
	if !oauthserver.ScopeIsSubset(scope, strings.Join(client.AllowedScopes, " ")) {
		writeServiceError(w, http.StatusBadRequest, "invalid_scope", "Requested scope exceeds what this client is allowed to request.", nil)
		return
	}

	consentedScope, hasConsent, err := oauthserver.GetConsentScope(r.Context(), state.Pool, client.ID, userID)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	alreadyConsented := hasConsent && oauthserver.ScopeIsSubset(scope, consentedScope)

	if alreadyConsented {
		redirectTo, err := issueAuthorizationCode(r.Context(), state, client.ID, tenantID, userID, redirectURI, scope,
			codeChallenge, codeChallengeMethod, clientState)
		if err != nil {
			writeInternalError(w, err)
			return
		}
		http.Redirect(w, r, redirectTo, http.StatusFound)
		return
	}

	pending, err := oauthserver.CreatePendingAuthorization(r.Context(), state.Pool, oauthserver.CreatePendingAuthorizationInput{
		ClientID: client.ID, TenantID: tenantID, UserID: userID, RedirectURI: redirectURI, Scope: scope,
		CodeChallenge: codeChallenge, CodeChallengeMethod: codeChallengeMethod, ClientState: clientState,
	})
	if err != nil {
		writeInternalError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(consentPageHTML(client.Name, scope, pending.ID)))
}

type oauthDecisionBody struct {
	RequestID uuid.UUID `json:"requestId"`
	Approve   bool      `json:"approve"`
}

// authorizeDecision is the consent page's approve/deny action. Requires the same live session
// that saw the consent screen — ConsumePendingAuthorization matches tenantID/userID in its own
// query, so a pending authorization created for one user can't be decided by another, even
// within the same tenant.
func authorizeDecision(w http.ResponseWriter, r *http.Request, state *AppState) {
	rc, ok := requireAuth(w, r, state)
	if !ok {
		return
	}
	var body oauthDecisionBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeServiceError(w, http.StatusBadRequest, "validation_failed", "Invalid request body.", nil)
		return
	}
	tenantID, err := state.Permissions.ScopedTenant(rc)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	userID, err := uuid.Parse(rc.UserID)
	if err != nil {
		writeServiceError(w, http.StatusUnauthorized, "unauthorized", "A user session is required.", nil)
		return
	}

	// tenantID/userID are matched inside the query itself — a pending request belonging to a
	// different user in the same tenant comes back as nil here, the same as an unknown/expired
	// id, rather than a distinguishable error.
	pending, err := oauthserver.ConsumePendingAuthorization(r.Context(), state.Pool, body.RequestID, tenantID, userID)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	if pending == nil {
		writeServiceError(w, http.StatusBadRequest, "invalid_request",
			"This authorization request has expired or was already decided. Please start over.", nil)
		return
	}

	if !body.Approve {
		writeJSON(w, http.StatusOK, map[string]any{"redirectTo": deniedRedirect(pending.RedirectURI, pending.ClientState)})
		return
	}

	if err := oauthserver.RecordConsent(r.Context(), state.Pool, tenantID, pending.ClientID, userID, pending.Scope); err != nil {
		writeInternalError(w, err)
		return
	}
	redirectTo, err := issueAuthorizationCode(r.Context(), state, pending.ClientID, tenantID, userID,
		pending.RedirectURI, pending.Scope, pending.CodeChallenge, pending.CodeChallengeMethod, pending.ClientState)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"redirectTo": redirectTo})
}

// extractClientAuth reads client_id/client_secret from Authorization: Basic (RFC 6749 §2.3.1's
// preferred form) first, falling back to form fields — real client libraries use either.
func extractClientAuth(r *http.Request) (string, string, bool) {
	if header := r.Header.Get("Authorization"); header != "" {
		if b64, ok := strings.CutPrefix(header, "Basic "); ok {
			if decoded, err := base64.StdEncoding.DecodeString(b64); err == nil {
				if id, secret, found := strings.Cut(string(decoded), ":"); found {
					return id, secret, true
				}
			}
		}
	}
	id, secret := r.FormValue("client_id"), r.FormValue("client_secret")
	if id != "" && secret != "" {
		return id, secret, true
	}
	return "", "", false
}

func authenticateOAuthClient(w http.ResponseWriter, r *http.Request, state *AppState, clientID, clientSecret string) (*oauthserver.ClientWithSecret, bool) {
	client, err := oauthserver.GetClientByClientID(r.Context(), state.Pool, clientID)
	if err != nil {
		writeInternalError(w, err)
		return nil, false
	}
	if client == nil {
		writeServiceError(w, http.StatusUnauthorized, "invalid_client", "Unknown client.", nil)
		return nil, false
	}
	if !oauthserver.VerifyClientSecret(client, clientSecret) {
		writeServiceError(w, http.StatusUnauthorized, "invalid_client", "Invalid client credentials.", nil)
		return nil, false
	}
	return client, true
}

// oauthTokenResponse is RFC 6749 §5.1's exact response shape — no {"data": ...} envelope, a
// deliberate exception to this package's own REST convention everywhere else, because a real
// OAuth2 client library parses this response against the spec's shape, not this platform's.
type oauthTokenResponse struct {
	AccessToken  string  `json:"access_token"`
	TokenType    string  `json:"token_type"`
	ExpiresIn    uint64  `json:"expires_in"`
	RefreshToken *string `json:"refresh_token,omitempty"`
	Scope        string  `json:"scope"`
}

func tokenEndpoint(w http.ResponseWriter, r *http.Request, state *AppState) {
	if err := r.ParseForm(); err != nil {
		writeServiceError(w, http.StatusBadRequest, "invalid_request", "Invalid request body.", nil)
		return
	}
	clientIDStr, clientSecret, ok := extractClientAuth(r)
	if !ok {
		writeServiceError(w, http.StatusUnauthorized, "invalid_client", "Client authentication is required.", nil)
		return
	}
	client, ok := authenticateOAuthClient(w, r, state, clientIDStr, clientSecret)
	if !ok {
		return
	}
	if client.Client.RevokedAt != nil {
		writeServiceError(w, http.StatusUnauthorized, "invalid_client", "This client has been revoked.", nil)
		return
	}

	switch r.FormValue("grant_type") {
	case "authorization_code":
		authorizationCodeGrant(w, r, state, client)
	case "refresh_token":
		refreshTokenGrant(w, r, state, client)
	case "client_credentials":
		clientCredentialsGrant(w, state, client)
	default:
		writeServiceError(w, http.StatusBadRequest, "unsupported_grant_type",
			"Only authorization_code, refresh_token, and client_credentials are supported.", nil)
	}
}

// clientCredentialsGrant is RFC 6749 §4.4 — no resource owner in the loop, the client acts as
// itself. Mints a token for client.ServiceUserID (eagerly provisioned at
// POST /admin/oauth/clients time, nil only for a client registered before this grant existed)
// rather than any particular tenant user. No refresh token is issued (§4.4.3) — the client
// already holds its own long-lived credential and can request a fresh access token the same way
// at any time.
func clientCredentialsGrant(w http.ResponseWriter, state *AppState, client *oauthserver.ClientWithSecret) {
	if !client.Client.IsConfidential {
		writeServiceError(w, http.StatusBadRequest, "unauthorized_client", "client_credentials requires a confidential client.", nil)
		return
	}
	if client.Client.ServiceUserID == nil {
		writeServiceError(w, http.StatusBadRequest, "unauthorized_client",
			"This client has no service account provisioned; re-register it to use client_credentials.", nil)
		return
	}
	requestedScope := ""
	allowedScope := strings.Join(client.Client.AllowedScopes, " ")
	if !oauthserver.ScopeIsSubset(requestedScope, allowedScope) {
		writeServiceError(w, http.StatusBadRequest, "invalid_scope", "Requested scope exceeds what this client is allowed to request.", nil)
		return
	}
	accessToken, err := state.MintOAuthToken(client.Client.TenantID, *client.Client.ServiceUserID,
		oauthAccessTokenTTLSeconds, requestedScope, client.Client.ClientID)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, oauthTokenResponse{
		AccessToken: accessToken, TokenType: "Bearer", ExpiresIn: oauthAccessTokenTTLSeconds, Scope: requestedScope,
	})
}

func authorizationCodeGrant(w http.ResponseWriter, r *http.Request, state *AppState, client *oauthserver.ClientWithSecret) {
	code, redirectURI := r.FormValue("code"), r.FormValue("redirect_uri")
	if code == "" || redirectURI == "" {
		writeServiceError(w, http.StatusBadRequest, "invalid_request", "code and redirect_uri are required.", nil)
		return
	}

	record, err := oauthserver.ConsumeAuthorizationCode(r.Context(), state.Pool, code, client.Client.ID, redirectURI)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	if record == nil {
		writeServiceError(w, http.StatusBadRequest, "invalid_grant", "Invalid, expired, or already-used authorization code.", nil)
		return
	}

	// authorize's own check already refuses a public client no code_challenge, so reaching here
	// with record.CodeChallenge unset means the client is confidential and simply chose not to
	// use PKCE, which RFC 7636 allows.
	if record.CodeChallenge != nil {
		verifier := r.FormValue("code_verifier")
		if verifier == "" {
			writeServiceError(w, http.StatusBadRequest, "invalid_grant", "code_verifier is required for this authorization code.", nil)
			return
		}
		if !oauthserver.VerifyPKCE(verifier, *record.CodeChallenge) {
			writeServiceError(w, http.StatusBadRequest, "invalid_grant", "code_verifier does not match code_challenge.", nil)
			return
		}
	}

	accessToken, err := state.MintOAuthToken(record.TenantID, record.UserID, oauthAccessTokenTTLSeconds, record.Scope, client.Client.ClientID)
	if err != nil {
		writeInternalError(w, err)
		return
	}

	tx, err := state.Pool.Begin(r.Context())
	if err != nil {
		writeInternalError(w, err)
		return
	}
	_, rawRefresh, err := oauthserver.CreateRefreshToken(r.Context(), tx, oauthserver.CreateRefreshInput{
		ClientID: client.Client.ID, TenantID: record.TenantID, UserID: record.UserID, Scope: record.Scope,
	})
	if err != nil {
		tx.Rollback(r.Context()) //nolint:errcheck
		writeInternalError(w, err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeInternalError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, oauthTokenResponse{
		AccessToken: accessToken, TokenType: "Bearer", ExpiresIn: oauthAccessTokenTTLSeconds,
		RefreshToken: &rawRefresh, Scope: record.Scope,
	})
}

func refreshTokenGrant(w http.ResponseWriter, r *http.Request, state *AppState, client *oauthserver.ClientWithSecret) {
	rawRefresh := r.FormValue("refresh_token")
	if rawRefresh == "" {
		writeServiceError(w, http.StatusBadRequest, "invalid_request", "refresh_token is required.", nil)
		return
	}

	tx, err := state.Pool.Begin(r.Context())
	if err != nil {
		writeInternalError(w, err)
		return
	}
	outcome, err := oauthserver.ConsumeRefreshToken(r.Context(), tx, rawRefresh, client.Client.ID)
	if err != nil {
		tx.Rollback(r.Context()) //nolint:errcheck
		writeInternalError(w, err)
		return
	}

	switch outcome.Kind {
	case oauthserver.RefreshInvalid:
		tx.Rollback(r.Context()) //nolint:errcheck
		writeServiceError(w, http.StatusBadRequest, "invalid_grant", "Invalid, expired, or revoked refresh token.", nil)
	case oauthserver.RefreshReused:
		// The chain revocation already happened inside ConsumeRefreshToken — commit so it
		// sticks, then report the failure.
		if err := tx.Commit(r.Context()); err != nil {
			writeInternalError(w, err)
			return
		}
		writeServiceError(w, http.StatusBadRequest, "invalid_grant",
			"Refresh token reuse detected; this client's token chain has been revoked. The user must re-authorize.", nil)
	default: // RefreshRotated
		requestedScope := r.FormValue("scope")
		if requestedScope == "" {
			requestedScope = outcome.Record.Scope
		}
		if !oauthserver.ScopeIsSubset(requestedScope, outcome.Record.Scope) {
			tx.Rollback(r.Context()) //nolint:errcheck
			writeServiceError(w, http.StatusBadRequest, "invalid_scope", "Requested scope exceeds the originally granted scope.", nil)
			return
		}
		accessToken, err := state.MintOAuthToken(outcome.Record.TenantID, outcome.Record.UserID, oauthAccessTokenTTLSeconds,
			requestedScope, client.Client.ClientID)
		if err != nil {
			tx.Rollback(r.Context()) //nolint:errcheck
			writeInternalError(w, err)
			return
		}
		if err := tx.Commit(r.Context()); err != nil {
			writeInternalError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, oauthTokenResponse{
			AccessToken: accessToken, TokenType: "Bearer", ExpiresIn: oauthAccessTokenTTLSeconds,
			RefreshToken: &outcome.RawToken, Scope: requestedScope,
		})
	}
}

func revokeEndpoint(w http.ResponseWriter, r *http.Request, state *AppState) {
	if err := r.ParseForm(); err != nil {
		writeServiceError(w, http.StatusBadRequest, "invalid_request", "Invalid request body.", nil)
		return
	}
	clientIDStr, clientSecret, ok := extractClientAuth(r)
	if !ok {
		writeServiceError(w, http.StatusUnauthorized, "invalid_client", "Client authentication is required.", nil)
		return
	}
	client, ok := authenticateOAuthClient(w, r, state, clientIDStr, clientSecret)
	if !ok {
		return
	}
	token := r.FormValue("token")
	// Errors here still degrade to success per RFC 7009 §2.2 — this only turns a genuine
	// infrastructure failure into a 500 the caller can retry, not "token not found" into one.
	if err := oauthserver.RevokeRefreshToken(r.Context(), state.Pool, token, client.Client.ID); err != nil {
		writeInternalError(w, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// discoveryMetadata is RFC 8414 — endpoint paths only, not absolute URIs: this platform has no
// configured public base-URL surface today, so a strictly spec-conforming absolute-URI issuer/
// *_endpoint would need a new config key this pass doesn't add. A caller resolves these against
// whatever origin it already reached this document on, which is what every real client actually
// does in practice.
func discoveryMetadata(w http.ResponseWriter, r *http.Request, state *AppState) {
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                peripherals.JWTIssuer,
		"authorization_endpoint":                "/oauth/authorize",
		"token_endpoint":                        "/oauth/token",
		"revocation_endpoint":                   "/oauth/revoke",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token", "client_credentials"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"client_secret_basic", "client_secret_post"},
	})
}

// -------------------------------------------------------------------------------------------
// Admin client management — recovered from git history (before it moved to GraphQL)
// -------------------------------------------------------------------------------------------

type createOAuthClientBody struct {
	Name           string   `json:"name"`
	RedirectURIs   []string `json:"redirectUris"`
	AllowedScopes  []string `json:"allowedScopes,omitempty"`
	IsConfidential *bool    `json:"isConfidential,omitempty"`
}

func oauthClientToJSON(client *oauthserver.OAuthClient) map[string]any {
	var serviceUserID any
	if client.ServiceUserID != nil {
		serviceUserID = *client.ServiceUserID
	}
	return map[string]any{
		"id": client.ID, "clientId": client.ClientID, "name": client.Name,
		"redirectUris": client.RedirectURIs, "allowedScopes": client.AllowedScopes,
		"isConfidential": client.IsConfidential, "serviceUserId": serviceUserID,
	}
}

// createOAuthClientAdmin returns the raw clientSecret — the only response that ever will, same
// write-once discipline oauthserver.CreateClient's doc comment describes. Losing it means
// revoking this client and registering a new one, not "look it up again". Also provisions this
// client's client_credentials service user before creating the client row, so the two can't
// disagree — the synthetic external_subject/email are pure uniqueness keys, never shown to
// anyone or used to authenticate.
func createOAuthClientAdmin(w http.ResponseWriter, r *http.Request, state *AppState) {
	rc, ok := requireAdmin(w, r, state)
	if !ok {
		return
	}
	var body createOAuthClientBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeServiceError(w, http.StatusBadRequest, "validation_failed", "Invalid request body.", nil)
		return
	}
	tenantID, err := state.Permissions.ScopedTenant(rc)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	isConfidential := body.IsConfidential == nil || *body.IsConfidential

	externalSubject := "oauth-client-" + uuid.New().String()
	serviceUser, err := tenantauth.JitProvisionExternalUser(r.Context(), state.Pool, tenantID, "oauth2_client_credentials",
		externalSubject+"@service.internal", externalSubject)
	if err != nil {
		writeInternalError(w, err)
		return
	}

	client, secret, err := oauthserver.CreateClient(r.Context(), state.Pool, oauthserver.CreateClientInput{
		TenantID: tenantID, Name: body.Name, RedirectURIs: body.RedirectURIs, AllowedScopes: body.AllowedScopes,
		IsConfidential: isConfidential, ServiceUserID: serviceUser.ID,
	})
	if err != nil {
		writeInternalError(w, err)
		return
	}
	dto := oauthClientToJSON(client)
	dto["clientSecret"] = secret
	writeJSON(w, http.StatusCreated, map[string]any{"data": dto})
}

func listOAuthClientsAdmin(w http.ResponseWriter, r *http.Request, state *AppState) {
	rc, ok := requireAdmin(w, r, state)
	if !ok {
		return
	}
	tenantID, err := state.Permissions.ScopedTenant(rc)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	clients, err := oauthserver.ListClients(r.Context(), state.Pool, tenantID)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	data := make([]map[string]any, len(clients))
	for i, c := range clients {
		data[i] = oauthClientToJSON(c)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data})
}

func revokeOAuthClientAdmin(w http.ResponseWriter, r *http.Request, state *AppState) {
	rc, ok := requireAdmin(w, r, state)
	if !ok {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeServiceError(w, http.StatusNotFound, "not_found", "No such client in your tenant.", nil)
		return
	}
	tenantID, err := state.Permissions.ScopedTenant(rc)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	revoked, err := oauthserver.RevokeClient(r.Context(), state.Pool, tenantID, id)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	if !revoked {
		writeServiceError(w, http.StatusNotFound, "not_found", "No such client in your tenant.", nil)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func mountOAuth2(r chi.Router, state *AppState) {
	wrap := func(h func(http.ResponseWriter, *http.Request, *AppState)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) { h(w, r, state) }
	}
	r.Get("/oauth/authorize", wrap(authorizeOAuth2))
	r.Post("/oauth/authorize/decision", wrap(authorizeDecision))
	r.Post("/oauth/token", wrap(tokenEndpoint))
	r.Post("/oauth/revoke", wrap(revokeEndpoint))
	r.Get("/.well-known/oauth-authorization-server", wrap(discoveryMetadata))
	r.Post("/admin/oauth/clients", wrap(createOAuthClientAdmin))
	r.Get("/admin/oauth/clients", wrap(listOAuthClientsAdmin))
	r.Delete("/admin/oauth/clients/{id}", wrap(revokeOAuthClientAdmin))
}
