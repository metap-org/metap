package web

import (
	"context"
	"fmt"

	"github.com/metap-org/metap/internal/httpapi"
	"github.com/metap-org/metap/pkg/kit/httpx/router"
	"github.com/metap-org/metap/pkg/permission"
)

// Router is the router a service's routes are registered on (the one in a Mount).
type Router = router.Router

// A route a service writes itself says who may call it by the gate it is registered through, the
// same five groups metap's own routes sit in. Registering on the bare router is how an unguarded
// handler gets added by accident: nothing resolves the caller and nothing labels the route, so
// RoutesWithoutOneAccessRule (put it in the service's own test) reports it.
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
// "PATTERN [labels]". A service's own route test asserts it is empty after mounting its routes.
func RoutesWithoutOneAccessRule(r *Router) []string {
	rules := map[string]bool{
		httpapi.LabelPublic: true, httpapi.LabelAuthenticated: true, httpapi.LabelAdmin: true,
		httpapi.LabelPlatformAdmin: true, httpapi.LabelSelfAuth: true,
	}
	var bad []string
	for _, route := range r.Routes() {
		n := 0
		for _, l := range route.Labels {
			if rules[l] {
				n++
			}
		}
		if n != 1 {
			bad = append(bad, fmt.Sprintf("%s %v", route.Pattern, route.Labels))
		}
	}
	return bad
}
