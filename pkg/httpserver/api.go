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

// WriteCrudError answers a CrudService/RecordBackend failure: a *crud.ServiceError becomes the
// standard {"error":{...}} envelope with its status, code and field errors; anything else is an
// internal error. Custom routes use this instead of mapping the error themselves.
func WriteCrudError(w http.ResponseWriter, err error) { writeCrudError(w, err) }

// ParseOptionalUUID is the exported form of parseOptionalUUID: nil for "" or a malformed id.
func ParseOptionalUUID(s string) *uuid.UUID { return parseOptionalUUID(s) }

// Mount adds a group of routes to the router BuildRouter builds. Optional route groups
// (OAuth2 server, cron jobs, dashboards, attachments) and a service's own routes are Mounts.
type Mount func(r chi.Router, state *AppState)

// DecodeJSONBody decodes the request's JSON body into dst, bounded by httpx.DefaultMaxBodyBytes,
// and writes the failure response itself (413 payload_too_large, or 400 validation_failed). It
// reports whether the handler may continue. Every handler that takes a JSON body uses this, so
// no route reads an unbounded body or swallows a decode error.
func DecodeJSONBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	return decodeJSONBody(w, r, dst)
}
