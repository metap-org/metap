// Exported wrappers around this package's own request-auth/response-writing helpers, for an
// external, optional-capability package (e.g. a low-code admin API, mirroring the Rust original's
// separate `presenter` crate importing `metap_http::auth::{AuthContext, AdminContext,
// PlatformAdminContext}` across a crate boundary) to reuse without duplicating the auth/response
// logic. Every function here is a direct, behavior-preserving call-through to the private
// implementation already used by this package's own routes — no new logic.
package httpserver

import (
	"net/http"

	"github.com/metap-org/metap-go/permission"
)

// RequireAuth is the exported form of requireAuth.
func RequireAuth(w http.ResponseWriter, r *http.Request, state *AppState) (*permission.RequestContext, bool) {
	return requireAuth(w, r, state)
}

// RequireAdmin is the exported form of requireAdmin.
func RequireAdmin(w http.ResponseWriter, r *http.Request, state *AppState) (*permission.RequestContext, bool) {
	return requireAdmin(w, r, state)
}

// RequirePlatformAdmin is the exported form of requirePlatformAdmin.
func RequirePlatformAdmin(w http.ResponseWriter, r *http.Request, state *AppState) (*permission.RequestContext, bool) {
	return requirePlatformAdmin(w, r, state)
}

// WriteJSON is the exported form of writeJSON.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	writeJSON(w, status, v)
}

// WriteServiceError is the exported form of writeServiceError.
func WriteServiceError(w http.ResponseWriter, status int, errorCode, message string, fieldErrors map[string][]string) {
	writeServiceError(w, status, errorCode, message, fieldErrors)
}

// WriteInternalError is the exported form of writeInternalError.
func WriteInternalError(w http.ResponseWriter, err error) {
	writeInternalError(w, err)
}

// WriteRouterUnavailable is the exported form of writeRouterUnavailable.
func WriteRouterUnavailable(w http.ResponseWriter, err error) {
	writeRouterUnavailable(w, err)
}

// SessionCookies is the exported form of sessionCookies.
func SessionCookies(token, csrfValue string, ttlSeconds int, secure bool) (*http.Cookie, *http.Cookie) {
	return sessionCookies(token, csrfValue, ttlSeconds, secure)
}
