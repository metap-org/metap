// Virtual resources (roadmap 115 M6): permissions that belong to no entity — "view request data",
// "approve a change request". A product declares a resource with its own action names (from an
// init(), like an entity); an administrator grants those actions to roles exactly as they grant
// entity actions, and code asks CheckResource. Nothing here knows what a resource means.
//
// A resource's policies live in the same table as entity policies, under the key
// ResourcePrefix+name, so a resource can never collide with (or shadow) an entity of the same name.
package permission

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// ResourcePrefix starts the policy "entity" key of every virtual resource.
const ResourcePrefix = "resource:"

// ResourceKey is the policy key for resource name.
func ResourceKey(name string) string { return ResourcePrefix + name }

// ResourceNameFromKey is the inverse of ResourceKey; ok is false for an ordinary entity key.
func ResourceNameFromKey(key string) (string, bool) { return strings.CutPrefix(key, ResourcePrefix) }

var (
	resourceNamePattern   = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)*$`)
	resourceActionPattern = regexp.MustCompile(`^[a-z][A-Za-z0-9_]{0,60}$`)
)

// ResourceAction is one thing a role can be allowed to do on a resource.
type ResourceAction struct {
	Name        string
	Description string
}

// Resource is a declared virtual resource.
type Resource struct {
	Name        string
	Description string
	Actions     []ResourceAction
}

// HasAction reports whether the resource declares action.
func (r Resource) HasAction(action string) bool {
	for _, a := range r.Actions {
		if a.Name == action {
			return true
		}
	}
	return false
}

var (
	resourcesMu sync.RWMutex
	resources   = map[string]Resource{}
)

// DeclareResource registers a resource. It panics on a malformed or duplicate declaration, which
// is a programming error found at start-up, not a runtime condition.
func DeclareResource(r Resource) {
	if !resourceNamePattern.MatchString(r.Name) || len(r.Name) > 100 {
		panic(fmt.Sprintf("permission: invalid resource name %q (lower-case words joined by dots)", r.Name))
	}
	if len(r.Actions) == 0 {
		panic(fmt.Sprintf("permission: resource %q declares no actions", r.Name))
	}
	seen := map[string]bool{}
	for _, a := range r.Actions {
		if !resourceActionPattern.MatchString(a.Name) {
			panic(fmt.Sprintf("permission: resource %q has invalid action %q", r.Name, a.Name))
		}
		if seen[a.Name] {
			panic(fmt.Sprintf("permission: resource %q declares action %q twice", r.Name, a.Name))
		}
		seen[a.Name] = true
	}
	resourcesMu.Lock()
	defer resourcesMu.Unlock()
	if _, dup := resources[r.Name]; dup {
		panic(fmt.Sprintf("permission: resource %q declared twice", r.Name))
	}
	resources[r.Name] = r
}

// LookupResource returns a declared resource.
func LookupResource(name string) (Resource, bool) {
	resourcesMu.RLock()
	defer resourcesMu.RUnlock()
	r, ok := resources[name]
	return r, ok
}

// Resources lists every declared resource, by name.
func Resources() []Resource {
	resourcesMu.RLock()
	defer resourcesMu.RUnlock()
	out := make([]Resource, 0, len(resources))
	for _, r := range resources {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ValidResourceGrant reports whether (key, action) names a declared resource and one of its
// actions; false for a key that is not a resource key at all.
func ValidResourceGrant(key, action string) bool {
	name, ok := ResourceNameFromKey(key)
	if !ok {
		return false
	}
	r, found := LookupResource(name)
	return found && r.HasAction(action)
}

// ResourceScopeToken is an OAuth2 scope token that narrows a token to a resource action.
const ResourceScopeToken = "resource"

// ScopePermitsResource is ScopePermits for a virtual resource. An OAuth2 token must name it:
// "resource:*" (every resource action), "resource:<name>:*" or "resource:<name>:<action>". An
// ordinary session (no scope claim) is unaffected.
func (c *RequestContext) ScopePermitsResource(resource, action string) bool {
	tokens, ok := c.OAuthScope()
	if !ok {
		return true
	}
	for _, t := range tokens {
		switch t {
		case ResourceScopeToken + ":*", ResourceScopeToken + ":" + resource + ":*", ResourceScopeToken + ":" + resource + ":" + action:
			return true
		}
	}
	return false
}

// CheckResource says whether the caller may perform action on a declared virtual resource.
// Order matches entity checks: the OAuth2 scope first (it narrows even an admin), then the admin
// bypass, then the tenant's policies. An undeclared resource or action is an error — a typo in
// code must not silently deny (or allow) anything.
func (s *PermissionService) CheckResource(goCtx context.Context, ctx *RequestContext, resource, action string) (PermissionDecision, error) {
	return s.checkResource(goCtx, ctx, resource, action, nil)
}

// CheckResourceIn is CheckResource for an action that is about one scope, e.g. "view request data
// of zone Z": it also accepts a role held only on exactly that scope. A role held on another scope
// never counts.
func (s *PermissionService) CheckResourceIn(goCtx context.Context, ctx *RequestContext, resource, action string, scope ScopeRef) (PermissionDecision, error) {
	return s.checkResource(goCtx, ctx, resource, action, &scope)
}

func (s *PermissionService) checkResource(goCtx context.Context, ctx *RequestContext, resource, action string, scope *ScopeRef) (PermissionDecision, error) {
	def, ok := LookupResource(resource)
	if !ok {
		return PermissionDecision{}, fmt.Errorf("permission: resource %q is not declared", resource)
	}
	if !def.HasAction(action) {
		return PermissionDecision{}, fmt.Errorf("permission: resource %q has no action %q", resource, action)
	}
	if !ctx.ScopePermitsResource(resource, action) {
		return Forbidden(), nil
	}
	if ctx.IsAdmin() {
		return Allowed(), nil
	}
	tenantID, err := s.ScopedTenant(ctx)
	if err != nil {
		return PermissionDecision{}, err
	}
	rows, err := s.store.FindContextPolicies(goCtx, tenantID, ResourceKey(resource), action)
	if err != nil {
		return PermissionDecision{}, err
	}
	groups := ctx.RoleGroups()
	if EvaluatePolicies(rows, ctx, nil, false) == VerdictAllow {
		return Allowed(), nil
	}
	if scope == nil {
		return Forbidden(), nil
	}
	for _, g := range groups[1:] {
		if !g.coversScope(*scope) {
			continue
		}
		gctx := ctx.WithRoles(g.Roles)
		if gctx.IsAdmin() || EvaluatePolicies(rows, gctx, nil, false) == VerdictAllow {
			return Allowed(), nil
		}
	}
	return Forbidden(), nil
}

func (g RoleGroup) coversScope(want ScopeRef) bool {
	for _, s := range g.Scopes {
		if s == want {
			return true
		}
	}
	return false
}
