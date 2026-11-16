// Public POST /auth/login plus /auth/me, /auth/logout, /auth/token. OIDC/OAuth2 tenant-login
// provider flows (oidc_login/oidc_callback/oauth2_login/ oauth2_callback) are deferred to a later
// wave, same scope line the user drew for admin/ platform routes — local password is the only
// provider this core wave needs to exercise entity CRUD end to end.
package httpserver

import (
	"errors"
	"github.com/metap-org/metap/pkg/kit/errcode"
	"github.com/metap-org/metap/pkg/kit/httpx"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/metap-org/metap/pkg/config"
	"github.com/metap-org/metap/pkg/identity"
	"github.com/metap-org/metap/pkg/peripherals"
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

// login signs a person in (roadmap 113). The password is checked against the identity; the
// session then lands in one tenant: the one asked for (tenantId), else the person's only or last
// used tenant. A person in several tenants with none of those gets the list back instead of a
// session ({selectTenant: true, tenants}) and posts again with tenantId.
func login(w http.ResponseWriter, r *http.Request, state *AppState) {
	// Checked before the credentials, so a non-issuer never acts as a password oracle.
	if !state.CanIssueTokens() {
		writeServiceError(w, http.StatusNotImplemented, errcode.NotTokenIssuer, ErrNotTokenIssuer.Error(), nil)
		return
	}
	var body loginBody
	if !decodeJSONBody(w, r, &body) {
		return
	}
	now := time.Now()
	ident, err := identity.Authenticate(r.Context(), state.Pool, body.Email, body.Password, now)
	if err != nil {
		writeIdentityError(w, err)
		return
	}
	memberships, err := identity.Memberships(r.Context(), state.Pool, ident.ID)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	m, ok := identity.PickMembership(memberships, body.TenantID, ident.LastTenantID)
	if !ok {
		if len(memberships) == 0 {
			writeServiceError(w, http.StatusForbidden, errcode.NoTenantAccess,
				"This account has no access to any active tenant.", nil)
			return
		}
		if body.TenantID != nil {
			writeServiceError(w, http.StatusForbidden, errcode.NoTenantAccess,
				"This account has no access to that tenant.", nil)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{
			"selectTenant": true, "tenants": membershipsJSON(memberships),
		}})
		return
	}
	startSession(w, r, state, ident.ID, m, now)
}

func membershipsJSON(ms []identity.Membership) []map[string]any {
	out := make([]map[string]any, len(ms))
	for i, m := range ms {
		out[i] = map[string]any{"tenantId": m.TenantID, "name": nullableString(m.TenantName)}
	}
	return out
}

// startSession mints the session for membership m and sets the cookies — a fresh login session
// (its absolute-lifetime clock starts now), used by login and by switching tenant.
func startSession(w http.ResponseWriter, r *http.Request, state *AppState, identityID uuid.UUID, m identity.Membership, now time.Time) {
	ttl := sessionTTLSeconds(r, state, m.TenantID)
	token, err := state.MintToken(m.TenantID, m.MemberID, ttl)
	if err != nil {
		if errors.Is(err, ErrNotTokenIssuer) {
			writeServiceError(w, http.StatusNotImplemented, errcode.NotTokenIssuer, err.Error(), nil)
			return
		}
		writeInternalError(w, err)
		return
	}
	if err := identity.RecordSignIn(r.Context(), state.Pool, identityID, m.MemberID, m.TenantID, now); err != nil {
		writeInternalError(w, err)
		return
	}
	csrfValue := uuid.New().String()
	sessionCookie, csrfCookie := sessionCookies(token, csrfValue, int(ttl), state.CookieSecure)
	absoluteMax := state.EffectiveConfig(r.Context(), m.TenantID).GetU64(config.AuthSessionAbsoluteMaxSeconds)
	startedAtCookie := sessionStartedAtCookie(now.Unix(), int64(absoluteMax), state.CookieSecure)
	http.SetCookie(w, sessionCookie)
	http.SetCookie(w, csrfCookie)
	http.SetCookie(w, startedAtCookie)
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"token": token, "tenantId": m.TenantID}})
}

// writeIdentityError maps pkg/identity's sign-in and password errors onto the error envelope.
func writeIdentityError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, identity.ErrInvalidCredentials):
		writeServiceError(w, http.StatusUnauthorized, errcode.InvalidCredentials, "Invalid email or password.", nil)
	case errors.Is(err, identity.ErrLocked):
		writeServiceError(w, http.StatusTooManyRequests, errcode.AccountLocked, err.Error(), nil)
	case errors.Is(err, identity.ErrDisabled):
		writeServiceError(w, http.StatusForbidden, errcode.AccountDisabled, err.Error(), nil)
	case errors.Is(err, identity.ErrWeakPassword):
		writeServiceError(w, http.StatusBadRequest, errcode.WeakPassword, err.Error(),
			map[string][]string{"password": {err.Error()}})
	default:
		writeInternalError(w, err)
	}
}

type switchTenantBody struct {
	TenantID uuid.UUID `json:"tenantId"`
}

// switchTenant moves the caller's session to another tenant they are a member of, without
// asking for the password again. REST because it sets the session cookie.
func switchTenant(w http.ResponseWriter, r *http.Request, state *AppState) {
	if !state.CanIssueTokens() {
		writeServiceError(w, http.StatusNotImplemented, errcode.NotTokenIssuer, ErrNotTokenIssuer.Error(), nil)
		return
	}
	rc, ok := requireAuth(w, r, state)
	if !ok {
		return
	}
	var body switchTenantBody
	if !decodeJSONBody(w, r, &body) {
		return
	}
	identityID, ok := callerIdentity(w, r, state, rc)
	if !ok {
		return
	}
	memberships, err := identity.Memberships(r.Context(), state.Pool, identityID)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	m, found := identity.PickMembership(memberships, &body.TenantID, nil)
	if !found {
		writeServiceError(w, http.StatusForbidden, errcode.NoTenantAccess, "You are not a member of that tenant.", nil)
		return
	}
	startSession(w, r, state, identityID, m, time.Now())
}

// callerIdentity resolves the identity behind the caller's token; a token whose subject is not a
// person (a service principal) gets 403.
func callerIdentity(w http.ResponseWriter, r *http.Request, state *AppState, rc *permission.RequestContext) (uuid.UUID, bool) {
	tenantID, err1 := uuid.Parse(rc.TenantID)
	userID, err2 := uuid.Parse(rc.UserID)
	if err1 != nil || err2 != nil {
		writeServiceError(w, http.StatusForbidden, errcode.Forbidden, "This token does not belong to a person.", nil)
		return uuid.Nil, false
	}
	id, err := identity.IdentityOf(r.Context(), state.Pool, tenantID, userID)
	if errors.Is(err, identity.ErrNotFound) {
		writeServiceError(w, http.StatusForbidden, errcode.Forbidden, "This token does not belong to a person.", nil)
		return uuid.Nil, false
	}
	if err != nil {
		writeInternalError(w, err)
		return uuid.Nil, false
	}
	return id, true
}

type changePasswordBody struct {
	CurrentPassword string `json:"currentPassword"`
	NewPassword     string `json:"newPassword"`
}

// changePassword sets the caller's own password. It revokes every session of the person
// (all tenants, all devices) and hands this one a fresh session — REST because of that cookie.
func changePassword(w http.ResponseWriter, r *http.Request, state *AppState) {
	if !state.CanIssueTokens() {
		writeServiceError(w, http.StatusNotImplemented, errcode.NotTokenIssuer, ErrNotTokenIssuer.Error(), nil)
		return
	}
	rc, ok := requireAuth(w, r, state)
	if !ok {
		return
	}
	var body changePasswordBody
	if !decodeJSONBody(w, r, &body) {
		return
	}
	identityID, ok := callerIdentity(w, r, state, rc)
	if !ok {
		return
	}
	current, err := identity.CheckPassword(r.Context(), state.Pool, identityID, body.CurrentPassword)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	if !current {
		writeServiceError(w, http.StatusBadRequest, errcode.WrongPassword, "The current password is wrong.", nil)
		return
	}
	if err := identity.CheckPasswordPolicy(r.Context(), state.Pool, identityID, body.NewPassword, state.PasswordPolicy()); err != nil {
		writeIdentityError(w, err)
		return
	}
	now := time.Now()
	if err := identity.SetPassword(r.Context(), state.Pool, identityID, body.NewPassword, now); err != nil {
		writeIdentityError(w, err)
		return
	}
	tenantID, _ := uuid.Parse(rc.TenantID)
	userID, _ := uuid.Parse(rc.UserID)
	startSession(w, r, state, identityID, identity.Membership{MemberID: userID, TenantID: tenantID}, now)
}

func logout(w http.ResponseWriter, _ *http.Request, state *AppState) {
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
	rc, ok := requireAuth(w, r, state)
	if !ok {
		return
	}
	email := resolveOwnEmail(r, state, rc)
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
func tryRefreshSession(w http.ResponseWriter, r *http.Request, state *AppState, rc *permission.RequestContext) {
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

	tenantID, err := uuid.Parse(rc.TenantID)
	if err != nil {
		return
	}
	userID, err := uuid.Parse(rc.UserID)
	if err != nil {
		return
	}

	effective := state.EffectiveConfig(r.Context(), tenantID)
	absoluteMax := int64(effective.GetU64(config.AuthSessionAbsoluteMaxSeconds))
	if unixNowSecs()-startedAt >= absoluteMax {
		return
	}

	ttl := effective.GetU64(config.AuthSessionTTLSeconds)
	token, err := state.MintToken(tenantID, userID, ttl)
	if err != nil {
		return
	}
	session, csrf := sessionCookies(token, csrfCookie.Value, int(ttl), state.CookieSecure)
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
	rc, ok := requireAuth(w, r, state)
	if !ok {
		return
	}
	if !credentialIssuingRequestAllowed(r) {
		writeServiceError(w, http.StatusUnauthorized, errcode.Unauthorized, "Missing or invalid CSRF token.", nil)
		return
	}
	tenantID, err := uuid.Parse(rc.TenantID)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	userID, err := uuid.Parse(rc.UserID)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	token, err := state.MintToken(tenantID, userID, sessionTTLSeconds(r, state, tenantID))
	if err != nil {
		if errors.Is(err, ErrNotTokenIssuer) {
			writeServiceError(w, http.StatusNotImplemented, errcode.NotTokenIssuer, err.Error(), nil)
			return
		}
		writeInternalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]string{"token": token}})
}

func resolveOwnEmail(r *http.Request, state *AppState, rc *permission.RequestContext) any {
	userID, err := uuid.Parse(rc.UserID)
	if err != nil {
		return nil
	}
	tenantID, err := state.Permissions.ScopedTenant(rc)
	if err != nil {
		return nil
	}
	tx, err := state.Router.Begin(r.Context(), tenantID)
	if err != nil {
		return nil
	}
	defer tx.Rollback(r.Context()) //nolint:errcheck
	user, err := peripherals.FindUserByID(r.Context(), tx, tenantID, userID)
	if err != nil || user == nil {
		return nil
	}
	return user.Email
}

func mountAuth(r chi.Router, state *AppState) {
	wrap := func(h func(http.ResponseWriter, *http.Request, *AppState)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) { h(w, r, state) }
	}
	r.Post("/auth/login", wrap(login))
	r.Post("/auth/logout", wrap(logout))
	r.Post("/auth/switch-tenant", wrap(switchTenant))
	r.Post("/auth/password", wrap(changePassword))
	r.Get("/auth/me", wrap(me))
	r.Post("/auth/invitations/lookup", wrap(lookupInvitation))
	r.Post("/auth/invitations/accept", wrap(acceptInvitation))
	r.Post("/auth/password/forgot", wrap(forgotPassword))
	r.Post("/auth/password/reset", wrap(resetPassword))
	r.Get("/auth/token", wrap(issueToken))
}
