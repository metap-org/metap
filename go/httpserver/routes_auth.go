// Public POST /auth/login plus /auth/me, /auth/logout, /auth/token. Ported from the Rust
// routes/auth.rs — the local-password login path, sliding-session refresh, and token re-mint
// only. OIDC/OAuth2 tenant-login provider flows (oidc_login/oidc_callback/oauth2_login/
// oauth2_callback) are deferred to a later wave, same scope line the user drew for admin/
// platform routes — local password is the only provider this core wave needs to exercise entity
// CRUD end to end.
package httpserver

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/metap-org/metap-go/config"
	"github.com/metap-org/metap-go/peripherals"
	"github.com/metap-org/metap-go/permission"
	"github.com/metap-org/metap-go/runtime"
	"github.com/metap-org/metap-go/tenantauth"
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

func login(w http.ResponseWriter, r *http.Request, state *AppState) {
	var body loginBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeServiceError(w, http.StatusBadRequest, "validation_failed", "Invalid request body.", nil)
		return
	}

	local := tenantauth.LocalPasswordProvider{}
	var user *peripherals.AuthUser
	if body.TenantID != nil {
		tx, err := state.Router.Begin(r.Context(), *body.TenantID)
		if err != nil {
			writeRouterUnavailable(w, err)
			return
		}
		u, vErr := local.Verify(r.Context(), tx, body.Email, body.Password)
		if vErr != nil {
			tx.Rollback(r.Context()) //nolint:errcheck
			writeInternalError(w, vErr)
			return
		}
		if u != nil {
			if err := tx.Commit(r.Context()); err != nil {
				writeInternalError(w, err)
				return
			}
		} else {
			tx.Rollback(r.Context()) //nolint:errcheck
		}
		user = u
	} else {
		u, err := local.Verify(r.Context(), state.Pool, body.Email, body.Password)
		if err != nil {
			writeInternalError(w, err)
			return
		}
		user = u
	}

	if user == nil {
		writeServiceError(w, http.StatusUnauthorized, "invalid_credentials", "Invalid email or password.", nil)
		return
	}

	ttl := sessionTTLSeconds(r, state, user.TenantID)
	token, err := state.MintToken(user.TenantID, user.ID, ttl)
	if err != nil {
		writeInternalError(w, err)
		return
	}

	csrfValue := uuid.New().String()
	sessionCookie, csrfCookie := sessionCookies(token, csrfValue, int(ttl), state.CookieSecure)
	absoluteMax := state.EffectiveConfig(r.Context(), user.TenantID).GetU64(config.AuthSessionAbsoluteMaxSeconds)
	startedAtCookie := sessionStartedAtCookie(unixNowSecs(), int64(absoluteMax), state.CookieSecure)
	http.SetCookie(w, sessionCookie)
	http.SetCookie(w, csrfCookie)
	http.SetCookie(w, startedAtCookie)
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]string{"token": token}})
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
	sessionCookie, err := r.Cookie(runtime.SessionCookieName)
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
	csrfCookie, err := r.Cookie(runtime.CSRFCookieName)
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
	rc, ok := requireAuth(w, r, state)
	if !ok {
		return
	}
	if !credentialIssuingRequestAllowed(r) {
		writeServiceError(w, http.StatusUnauthorized, "unauthorized", "Missing or invalid CSRF token.", nil)
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
	r.Get("/auth/me", wrap(me))
	r.Get("/auth/token", wrap(issueToken))
}
