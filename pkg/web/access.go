package web

import (
	"context"

	"github.com/metap-org/metap/internal/httpapi"
	"github.com/metap-org/metap/pkg/kit/httpx/router"
	"github.com/metap-org/metap/pkg/permission"
)

// Router is the router a service's routes are registered on (the one in a Mount).
type Router = router.Router

// A route a service writes itself says who may call it by the gate it is registered through, the
// same five groups metap's own routes sit in. Registering on the bare router is how an unguarded
// handler gets added by accident: nothing resolves the caller and nothing labels the route. app.Run
// refuses to start with such a route (RoutesWithoutOneAccessRule lists them).
//
//	web.Authenticated(r, state).Post("/internal/jobs/{id}/results", h)
//
// The gate resolves the caller before the handler runs (read it with RC). A handler that needs
// more than "signed in" (a role on this very zone, say) still checks it itself, with CheckResourceIn.

// Public is for a route that needs no credentials (health, discovery).
func Public(r *Router, state *State) *Router { return httpapi.AccessGroups(r, state).Public }

// Authenticated is for a route any signed-in caller may use.
func Authenticated(r *Router, state *State) *Router {
	return httpapi.AccessGroups(r, state).Authenticated
}

// Admin is for a route only a tenant admin may use.
func Admin(r *Router, state *State) *Router { return httpapi.AccessGroups(r, state).Admin }

// PlatformAdmin is for a route only the platform's own admin may use.
func PlatformAdmin(r *Router, state *State) *Router {
	return httpapi.AccessGroups(r, state).PlatformAdmin
}

// SelfAuth is for a route that authenticates inside its handler because its caller is not a signed-in
// person (a callback with its own secret, another kind of bearer). The route is labelled so it is
// listed as deliberate, and the handler is responsible for refusing the wrong caller.
func SelfAuth(r *Router, state *State) *Router { return httpapi.AccessGroups(r, state).SelfAuth }

// CheckResourceIn is the decision about an action on a declared permission resource (see
// permission.DeclareResource) for one scope, e.g. "view request data of zone Z": it accepts a role
// held tenant-wide or only on exactly that scope. A role held on another scope never counts. An
// undeclared resource or action is an error, not a quiet deny.
func CheckResourceIn(ctx context.Context, state *State, rc *permission.RequestContext, resource, action string, scope permission.ScopeRef) (permission.PermissionDecision, error) {
	return state.Permissions.CheckResourceIn(ctx, rc, resource, action, scope)
}

// RoutesWithoutOneAccessRule lists the routes of r that do not sit in exactly one access group, as
// "PATTERN [labels]". app.Run refuses to start while there is one; a service's own route test can
// assert this is empty after mounting its routes, to learn that before the service is deployed.
func RoutesWithoutOneAccessRule(r *Router) []string { return httpapi.RoutesWithoutOneAccessRule(r) }
