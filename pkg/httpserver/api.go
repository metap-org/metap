// The exported surface for route modules that live outside this package (oauth2routes,
// cronroutes, dashboardroutes, attachmentroutes, and any downstream service's own routes): the
// request-auth/response-writing helpers this package's own routes use, as direct,
// behavior-preserving call-throughs — no new logic — plus the Mount type BuildRouter takes.
package httpserver

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/metap-org/metap/pkg/permission"
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

// WriteCrudServiceError is the exported form of writeCrudServiceError (crud.ServiceResult's
// error shape -> the standard {"error":{...}} response).
func WriteCrudServiceError(w http.ResponseWriter, status uint16, errorCode string, message *string, fieldErrors map[string][]string) {
	writeCrudServiceError(w, status, errorCode, message, fieldErrors)
}

// ParseOptionalUUID is the exported form of parseOptionalUUID: nil for "" or a malformed id.
func ParseOptionalUUID(s string) *uuid.UUID { return parseOptionalUUID(s) }

// Mount adds a group of routes to the router BuildRouter builds. Optional route groups
// (OAuth2 server, cron jobs, dashboards, attachments) and a service's own routes are Mounts.
type Mount func(r chi.Router, state *AppState)
