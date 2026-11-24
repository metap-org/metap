// Package web is the HTTP side of a service built on metap, as that service sees it: the state its
// routes and actions hang on, the helpers that answer in the standard error envelope, and the
// guards that put the signed-in caller in a request.
//
// The implementation is internal/httpapi (the routes, the typed administrative operations, SCIM,
// sign-in, sessions: nothing a service calls directly). This file is the whole public surface; a
// symbol is here because a service or a route module outside metap uses it. Everything is a thin
// call-through, so the behaviour is the one the routes in this repository have.
package web

import (
	"net/http"
	"sync/atomic"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/metap-org/metap/internal/httpapi"
	"github.com/metap-org/metap/pkg/metadata"
	"github.com/metap-org/metap/pkg/permission"
	"github.com/metap-org/metap/pkg/tenancy"
)

// State is what a service's routes and actions share: the database pool and tenant router, the
// record service, permissions, configuration, token signing and verification, and the hooks a
// service plugs its own behaviour into (WrapBackend's backend, Actions, ActionDeps).
type State = httpapi.AppState

// Mount registers a group of routes on the service's router.
type Mount = httpapi.Mount

// NewState builds the state of a service over its pool and tenant router.
func NewState(pool *pgxpool.Pool, metadataBase *metadata.MetadataRegistry, md *atomic.Pointer[metadata.MetadataRegistry],
	permissions *permission.PermissionService, jwtPrivateKeyPEM string, jwtPublicKey any, tenants *tenancy.Router) *State {
	return httpapi.NewAppState(pool, metadataBase, md, permissions, jwtPrivateKeyPEM, jwtPublicKey, tenants)
}

// RequireAuth returns the signed-in caller, or answers 401 itself and reports false.
func RequireAuth(w http.ResponseWriter, r *http.Request, state *State) (*permission.RequestContext, bool) {
	return httpapi.RequireAuth(w, r, state)
}

// RequireAdmin is RequireAuth for a tenant admin: it answers 401 or 403 itself and reports false.
func RequireAdmin(w http.ResponseWriter, r *http.Request, state *State) (*permission.RequestContext, bool) {
	return httpapi.RequireAdmin(w, r, state)
}

// RequirePlatformAdmin is RequireAuth for the platform's own admin.
func RequirePlatformAdmin(w http.ResponseWriter, r *http.Request, state *State) (*permission.RequestContext, bool) {
	return httpapi.RequirePlatformAdmin(w, r, state)
}

// RC is the caller a request passed through an authenticating group resolved; nil when it did not.
func RC(r *http.Request) *permission.RequestContext { return httpapi.RC(r) }

// WriteJSON answers status with v as JSON.
func WriteJSON(w http.ResponseWriter, status int, v any) { httpapi.WriteJSON(w, status, v) }

// WriteServiceError answers in the standard {"error":{code,message,fieldErrors}} envelope.
func WriteServiceError(w http.ResponseWriter, status int, errorCode, message string, fieldErrors map[string][]string) {
	httpapi.WriteServiceError(w, status, errorCode, message, fieldErrors)
}

// WriteInternalError answers a failure that is the service's own with the standard 500.
func WriteInternalError(w http.ResponseWriter, err error) { httpapi.WriteInternalError(w, err) }

// WriteRouterUnavailable answers a tenant that cannot be reached (suspended, migrating, deleted).
func WriteRouterUnavailable(w http.ResponseWriter, err error) { httpapi.WriteRouterUnavailable(w, err) }

// WriteCrudError answers a record-service failure: a *crud.ServiceError keeps its status, code and
// field errors; anything else is an internal error.
func WriteCrudError(w http.ResponseWriter, err error) { httpapi.WriteCrudError(w, err) }

// DecodeJSONBody reads a bounded JSON body into dst, answering 400 or 413 itself when it cannot.
func DecodeJSONBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	return httpapi.DecodeJSONBody(w, r, dst)
}

// SessionCookies is the session and CSRF cookie pair a sign-in sets.
func SessionCookies(token, csrfValue string, ttlSeconds int, secure bool) (*http.Cookie, *http.Cookie) {
	return httpapi.SessionCookies(token, csrfValue, ttlSeconds, secure)
}

// ParseOptionalUUID is nil for "" or a malformed id.
func ParseOptionalUUID(s string) *uuid.UUID { return httpapi.ParseOptionalUUID(s) }
