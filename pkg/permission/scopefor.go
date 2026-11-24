package permission

import (
	"context"
	"sort"

	"github.com/metap-org/metap/pkg/kit/scope"
)

// ScopeFor is the scope of the data a caller may read from a store outside metap, for one action on
// a declared resource (roadmap 121). It follows the same policies as CheckResource and
// CheckResourceIn and returns:
//
//   - the whole tenant, limited on nothing, when the caller may do the action tenant-wide (the admin
//     role, or a role of theirs that the tenant's policies grant it to);
//   - otherwise, a tenant scope limited on each scope type (a zone, an account) to exactly the scopes
//     where a role they hold only there is granted it;
//   - otherwise, a refusal: the decision is not allowed and the scope is the zero Scope, which no
//     adapter accepts.
//
// An adapter reads through the Scope it is handed; it never receives a bare tenant.
func (s *PermissionService) ScopeFor(ctx context.Context, rc *RequestContext, resource, action string) (scope.Scope, PermissionDecision, error) {
	tenant, err := s.ScopedTenant(rc)
	if err != nil {
		return scope.Scope{}, PermissionDecision{}, err
	}
	wide, err := s.CheckResource(ctx, rc, resource, action)
	if err != nil {
		return scope.Scope{}, PermissionDecision{}, err
	}
	if wide.Allowed {
		return scope.ForTenant(tenant.String()), Allowed(), nil
	}

	// No tenant-wide grant: collect the scopes the caller holds a granting role on, by scope type.
	seen := map[ScopeRef]bool{}
	granted := map[string][]string{}
	for _, group := range rc.RoleGroups() {
		for _, ref := range group.Scopes {
			if seen[ref] {
				continue
			}
			seen[ref] = true
			d, err := s.CheckResourceIn(ctx, rc, resource, action, ref)
			if err != nil {
				return scope.Scope{}, PermissionDecision{}, err
			}
			if d.Allowed {
				granted[ref.Type] = append(granted[ref.Type], ref.ID)
			}
		}
	}
	if len(granted) == 0 {
		return scope.Scope{}, Forbidden(), nil
	}
	types := make([]string, 0, len(granted))
	for t := range granted {
		types = append(types, t)
	}
	sort.Strings(types)
	out := scope.ForTenant(tenant.String())
	for _, t := range types {
		out = out.Limit(t, granted[t]...)
	}
	return out, Allowed(), nil
}
