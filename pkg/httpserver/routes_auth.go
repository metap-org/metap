// Public POST /auth/login plus /auth/me, /auth/logout, /auth/token. OIDC/OAuth2 tenant-login
// provider flows (oidc_login/oidc_callback/oauth2_login/ oauth2_callback) are deferred to a later
// wave, same scope line the user drew for admin/ platform routes — local password is the only
// provider this core wave needs to exercise entity CRUD end to end.
package httpserver

import (
	"github.com/metap-org/metap/pkg/kit/errcode"
	"github.com/metap-org/metap/pkg/kit/httpx"
	"github.com/metap-org/metap/pkg/kit/standard"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/metap-org/metap/pkg/kit/httpx/router"

	"github.com/metap-org/metap/internal/signin"
	"github.com/metap-org/metap/pkg/auth/identity"
	"github.com/metap-org/metap/pkg/config"
	"github.com/metap-org/metap/pkg/httpmw"
	"github.com/metap-org/metap/pkg/permission"
)

func sessionTTLSeconds(ctx *http.Request, state *AppState, tenantID uuid.UUID) uint64 {
	return state.EffectiveConfig(ctx.Context(), tenantID).GetU64(config.AuthSessionTTLSeconds)
}

func unixNowSecs() int64 { return time.Now().Unix() }

type loginBody struct {
	Email    string     `json:"email"`
	Password string     `json:"password"`
	TenantID *uuid.UUID `json:"tenantId,omitempty"`
}

// clientOf is where the request came from, for the session row.
func clientOf(r *http.Request, state *AppState) signin.Client {
	return signin.Client{IP: clientIP(r, state), UserAgent: r.UserAgent()}
}

// requireIssuer answers 501 and returns false on a service that holds no signing key.
func requireIssuer(w http.ResponseWriter, state *AppState) bool {
	if state.CanIssueTokens() {
		return true
	}
	writeServiceError(w, http.StatusNotImplemented, errcode.NotTokenIssuer, ErrNotTokenIssuer.Error(), nil)
	return false
}

// login signs a person in (roadmap 113): see signin.Service.Login. A person in several tenants with
// none asked for gets the list back instead of a session ({selectTenant: true, tenants}) and posts
// again with tenantId.
func login(w http.ResponseWriter, r *http.Request, state *AppState) {
	// Checked before the credentials, so a non-issuer never acts as a password oracle.
	if !requireIssuer(w, state) {
		return
	}
	var body loginBody
	if !decodeJSONBody(w, r, &body) {
		return
	}
	out, err := state.Signin().Login(r.Context(), signin.LoginRequest{Email: body.Email, Password: body.Password, TenantID: body.TenantID}, clientOf(r, state))
	if err != nil {
		writeCrudError(w, err)
		return
	}
	writeOutcome(w, state, out)
}

// writeOutcome answers a sign-in: the session (cookies and token), or the step it still needs.
func writeOutcome(w http.ResponseWriter, state *AppState, out *signin.Outcome) {
	switch {
	case out.Session != nil:
		writeGrant(w, state, out.Session)
	case out.MFARequired != nil:
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{
			"mfaRequired": true, "mfaToken": out.MFARequired.Token, "tenantId": out.MFARequired.TenantID}})
	case out.MFASetupRequired != nil:
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{
			"mfaSetupRequired": true, "mfaToken": out.MFASetupRequired.Token, "tenantId": out.MFASetupRequired.TenantID}})
	default:
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{
			"selectTenant": true, "tenants": membershipsJSON(out.SelectTenant),
		}})
	}
}

func membershipsJSON(ms []identity.Membership) []map[string]any {
	out := make([]map[string]any, len(ms))
	for i, m := range ms {
		out[i] = map[string]any{"tenantId": m.TenantID, "name": nullableString(m.TenantName)}
	}
	return out
}

// setSessionCookies sets the session, CSRF and started-at cookies that carry a Grant.
func setSessionCookies(w http.ResponseWriter, state *AppState, g *signin.Grant) {
	sessionCookie, csrfCookie := sessionCookies(g.Token, uuid.New().String(), int(g.TTLSeconds), state.CookieSecure)
	http.SetCookie(w, sessionCookie)
	http.SetCookie(w, csrfCookie)
	http.SetCookie(w, sessionStartedAtCookie(g.StartedAt.Unix(), int64(g.AbsoluteMaxSeconds), state.CookieSecure))
}

// writeGrant sets the cookies and answers {token, tenantId} (plus the recovery codes shown once when
// a required-MFA setup finishes the sign-in).
func writeGrant(w http.ResponseWriter, state *AppState, g *signin.Grant) {
	setSessionCookies(w, state, g)
	data := map[string]any{"token": g.Token, "tenantId": g.TenantID}
	if g.RecoveryCodes != nil {
		data["recoveryCodes"] = g.RecoveryCodes
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data})
}

// startSession signs the person into membership m (a fresh login session) and answers with it; used
// by the paths that prove who someone is some other way (invitation, signup verification).
func startSession(w http.ResponseWriter, r *http.Request, state *AppState, identityID uuid.UUID, m identity.Membership) {
	g, err := state.Signin().Issue(r.Context(), identityID, m, clientOf(r, state))
	if err != nil {
		writeCrudError(w, err)
		return
	}
	writeGrant(w, state, g)
}

// writeIdentityError maps pkg/identity's sign-in and password errors onto the error envelope.
func writeIdentityError(w http.ResponseWriter, err error) {
	writeCrudError(w, signin.IdentityError(err))
}

type switchTenantBody struct {
	TenantID uuid.UUID `json:"tenantId"`
}

// switchTenant moves the caller's session to another tenant they are a member of, without
// asking for the password again. REST because it sets the session cookie.
func switchTenant(w http.ResponseWriter, r *http.Request, state *AppState) {
	if !requireIssuer(w, state) {
		return
	}
	var body switchTenantBody
	if !decodeJSONBody(w, r, &body) {
		return
	}
	g, err := state.Signin().SwitchTenant(r.Context(), RC(r), body.TenantID, clientOf(r, state))
	if err != nil {
		writeCrudError(w, err)
		return
	}
	writeGrant(w, state, g)
}

type changePasswordBody struct {
	CurrentPassword string `json:"currentPassword"`
	NewPassword     string `json:"newPassword"`
}

// changePassword sets the caller's own password. It revokes every session of the person
// (all tenants, all devices) and hands this one a fresh session — REST because of that cookie.
func changePassword(w http.ResponseWriter, r *http.Request, state *AppState) {
	if !requireIssuer(w, state) {
		return
	}
	var body changePasswordBody
	if !decodeJSONBody(w, r, &body) {
		return
	}
	g, err := state.Signin().ChangePassword(r.Context(), RC(r), body.CurrentPassword, body.NewPassword, clientOf(r, state))
	if err != nil {
		writeCrudError(w, err)
		return
	}
	writeGrant(w, state, g)
}

func logout(w http.ResponseWriter, r *http.Request, state *AppState) {
	// Ending the browser's session on the server too (not just dropping the cookie) means a copied
	// token stops working. Best effort: with no valid session there is nothing to end.
	if rc, aerr := authenticate(r.Context(), state, r); aerr == nil {
		state.Signin().EndSession(r.Context(), rc)
	}
	session, csrf, startedAt := clearSessionCookies(state.CookieSecure)
	http.SetCookie(w, session)
	http.SetCookie(w, csrf)
	http.SetCookie(w, startedAt)
	w.WriteHeader(http.StatusNoContent)
}

// me returns identity + roles for the caller's own token, and opportunistically slides the
// session forward (see tryRefreshSession) — the frontend's only way to know "am I an admin" for
// UI gating, since roles are never encoded on the JWT itself.
func me(w http.ResponseWriter, r *http.Request, state *AppState) {
	rc := RC(r)
	email := nullableString(state.Signin().OwnEmail(r.Context(), rc))
	tryRefreshSession(w, r, state, rc)

	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{
		"userId": nullableString(rc.UserID), "tenantId": rc.TenantID, "email": email, "roles": rc.Roles,
	}})
}

func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// tryRefreshSession is the sliding-session Part B: a cookie-authenticated caller still inside
// auth.sessionAbsoluteMaxSeconds of their original login gets a fresh auth.sessionTtlSeconds
// window on every /auth/me poll. Writes new Set-Cookie headers directly on success; does
// nothing on any of the several reasons not to refresh (no session cookie, no
// session_started_at, already past the absolute cap, parse failure) — me must keep answering
// identity/roles regardless.
//
// Only a request the cookie itself authenticated is refreshed. An Authorization header wins over
// the cookie in authenticate, so with both present rc is the header's caller — an API key's
// service account, or another member's token — and re-minting the cookie from it would quietly
// turn the browser's session into that caller's.
func tryRefreshSession(w http.ResponseWriter, r *http.Request, state *AppState, rc *permission.RequestContext) {
	if r.Header.Get(standard.HeaderAuthorization) != "" {
		return
	}
	sessionCookie, err := r.Cookie(httpx.SessionCookieName)
	if err != nil || sessionCookie.Value == "" {
		return
	}
	startedAtCookie, err := r.Cookie(sessionStartedAtCookieName)
	if err != nil {
		return
	}
	startedAt, err := strconv.ParseInt(startedAtCookie.Value, 10, 64)
	if err != nil {
		return
	}
	csrfCookie, err := r.Cookie(httpx.CSRFCookieName)
	if err != nil {
		return
	}

	renewal, ok := state.Signin().Renew(r.Context(), rc, time.Unix(startedAt, 0))
	if !ok {
		return
	}
	session, csrf := sessionCookies(renewal.Token, csrfCookie.Value, int(renewal.TTLSeconds), state.CookieSecure)
	http.SetCookie(w, session)
	http.SetCookie(w, csrf)
}

// issueToken mints a fresh, short-lived Bearer token for the caller's own identity — for a
// browser caller that needs to authenticate to a service which only speaks Bearer and has no
// cookie of its own to send (e.g. a separate gateway deployment). Additionally CSRF-gated
// despite being a GET, since it mints a credential.
func issueToken(w http.ResponseWriter, r *http.Request, state *AppState) {
	if !state.CanIssueTokens() {
		writeServiceError(w, http.StatusNotImplemented, errcode.NotTokenIssuer, ErrNotTokenIssuer.Error(), nil)
		return
	}
	rc := RC(r)
	if !credentialIssuingRequestAllowed(r) {
		writeServiceError(w, http.StatusUnauthorized, errcode.Unauthorized, "Missing or invalid CSRF token.", nil)
		return
	}
	token, err := state.Signin().IssueToken(r.Context(), rc)
	if err != nil {
		writeCrudError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]string{"token": token}})
}

// authGuard is the stricter per-IP bucket the routes someone guessing credentials, codes or tokens
// would hammer share (a guesser cannot multiply the allowance by spreading over routes). The account
// lockout counts per identity; this counts per source, so one address cannot walk through many
// identities or many reset tokens either. One instance, so every route that uses it shares it.
func authGuard(state *AppState) router.Middleware {
	cfg := state.Config.Current()
	return httpmw.RateLimitBehind(
		time.Duration(cfg.GetU64(config.HTTPAuthRateLimitPerMs))*time.Millisecond,
		int(cfg.GetU64(config.HTTPAuthRateLimitBurst)),
		state.TrustedProxies,
	)
}

func mountAuth(r *router.Router, state *AppState, guard router.Middleware) {
	wrap := func(h func(http.ResponseWriter, *http.Request, *AppState)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) { h(w, r, state) }
	}
	acc := AccessGroups(r, state)
	guarded := r.Labelled(LabelPublic, guard)
	guarded.Post("/auth/login", wrap(login))
	acc.Public.Get("/auth/signup/status", wrap(signupStatus))
	guarded.Post("/auth/signup", wrap(signup))
	guarded.Post("/auth/signup/verify", wrap(signupVerify))
	guarded.Post("/auth/login/mfa", wrap(loginMFA))
	acc.Public.Post("/auth/mfa/setup", wrap(mfaSetup))
	acc.Public.Post("/auth/mfa/setup/confirm", wrap(mfaSetupConfirm))
	acc.Public.Post("/auth/logout", wrap(logout))
	acc.Authenticated.Post("/auth/switch-tenant", wrap(switchTenant))
	acc.Authenticated.Post("/auth/password", wrap(changePassword))
	acc.Authenticated.Get("/auth/me", wrap(me))
	guarded.Post("/auth/invitations/lookup", wrap(lookupInvitation))
	guarded.Post("/auth/invitations/accept", wrap(acceptInvitation))
	guarded.Post("/auth/password/forgot", wrap(forgotPassword))
	guarded.Post("/auth/password/reset", wrap(resetPassword))
	guarded.Post("/auth/workspace-deletion/cancel", wrap(cancelWorkspaceDeletion))
	acc.Authenticated.Get("/auth/token", wrap(issueToken))
}

func uuidOf(s string) uuid.UUID {
	id, _ := uuid.Parse(s)
	return id
}
