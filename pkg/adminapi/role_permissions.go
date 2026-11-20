// A role's permissions across every entity (roadmap 114, P3): the role-first editor reads and
// replaces the role's basic grants in one call instead of one entity at a time.
//
// No policy cache to invalidate: every PermissionService metap builds reads the policy store
// fresh (NewPermissionService, no cache), so a saved grant applies on the next request in every
// service sharing the tenant's policies.
package adminapi

import (
	"context"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/metap-org/metap/pkg/audit"
	"github.com/metap-org/metap/pkg/crud"
	"github.com/metap-org/metap/pkg/kit/errcode"
	"github.com/metap-org/metap/pkg/peripherals"
	"github.com/metap-org/metap/pkg/permission"
)

// maxGrants bounds one SetRolePermissions call (entities × actions of a large app fit well inside).
const maxGrants = 5000

var workflowActionPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,100}$`)

// validGrantAction is a base policy action or transition:<workflow action>.
func validGrantAction(action string) bool {
	for _, a := range permission.AllEntityActions {
		if action == a.String() {
			return true
		}
	}
	prefix := permission.TransitionActionPolicy("")
	return strings.HasPrefix(action, prefix) && workflowActionPattern.MatchString(strings.TrimPrefix(action, prefix))
}

func grantKey(g peripherals.RoleGrant) string { return g.Entity + " " + g.Action }

func toPBPermissions(role string, grants []peripherals.RoleGrant, advanced []peripherals.AdvancedPolicy) *RolePermissions {
	out := &RolePermissions{Role: role}
	for _, g := range grants {
		out.Grants = append(out.Grants, &RoleGrant{Entity: g.Entity, Action: g.Action})
	}
	for _, p := range advanced {
		out.Advanced = append(out.Advanced, &RolePolicy{Id: p.ID.String(), Entity: p.Entity, Action: p.Action,
			Roles: p.Roles, Effect: p.Effect, Field: p.Field, Subject: p.Subject, Condition: jsonOr(p.Condition, "null")})
	}
	return out
}

func (s *RoleAdminService) permissions(ctx context.Context, tenant uuid.UUID, name string) (*RolePermissions, error) {
	var grants []peripherals.RoleGrant
	var advanced []peripherals.AdvancedPolicy
	err := s.d.withTenantTx(ctx, tenant, func(db peripherals.DBTX) error {
		if err := peripherals.EnsureBuiltinRoles(ctx, db, tenant); err != nil {
			return err
		}
		if _, err := peripherals.GetRoleByName(ctx, db, tenant, name); err != nil {
			return err
		}
		var err error
		if grants, err = peripherals.RoleBasicGrants(ctx, db, tenant, name); err != nil {
			return err
		}
		advanced, err = peripherals.AdvancedPoliciesNamingRole(ctx, db, tenant, name)
		return err
	})
	if err != nil {
		return nil, err
	}
	return toPBPermissions(name, grants, advanced), nil
}

func (s *RoleAdminService) GetRolePermissions(ctx context.Context, req *RoleNameRequest) (*RolePermissions, error) {
	c, err := s.d.admin(ctx)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(req.GetName())
	out, err := s.permissions(ctx, c.tenant, name)
	if err != nil {
		return nil, roleErr(ctx, err, name)
	}
	return out, nil
}

func (s *RoleAdminService) SetRolePermissions(ctx context.Context, req *SetRolePermissionsRequest) (*RolePermissions, error) {
	c, err := s.d.admin(ctx)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(req.GetRole())
	if name == permission.RoleAdmin {
		return nil, svcErr(ctx, http.StatusBadRequest, errcode.ValidationFailed,
			"The admin role bypasses every policy; it has no permissions to set.")
	}
	if len(req.GetGrants()) > maxGrants {
		return nil, svcErr(ctx, http.StatusBadRequest, errcode.ValidationFailed, "Too many permissions in one call.")
	}
	grants := make([]peripherals.RoleGrant, 0, len(req.GetGrants()))
	for _, g := range req.GetGrants() {
		entity, action := strings.TrimSpace(g.GetEntity()), strings.TrimSpace(g.GetAction())
		// A virtual resource (roadmap 115 M6) takes only the actions it declares; anything else only the
		// entity actions.
		valid := validGrantAction(action)
		if strings.HasPrefix(entity, permission.ResourcePrefix) {
			valid = permission.ValidResourceGrant(entity, action)
		}
		if entity == "" || len(entity) > 120 || !valid {
			return nil, sendServiceError(ctx, &crud.ServiceError{Status: http.StatusBadRequest, Code: errcode.ValidationFailed,
				Message:     "Invalid permission " + entity + " " + action + ".",
				FieldErrors: map[string][]string{"grants": {entity + " " + action}}})
		}
		grants = append(grants, peripherals.RoleGrant{Entity: entity, Action: action})
	}
	var by *uuid.UUID
	if c.self != uuid.Nil {
		self := c.self
		by = &self
	}
	var role peripherals.Role
	var before []peripherals.RoleGrant
	if err := s.d.withTenantTx(ctx, c.tenant, func(db peripherals.DBTX) error {
		var err error
		if role, err = peripherals.GetRoleByName(ctx, db, c.tenant, name); err != nil {
			return err
		}
		if before, err = peripherals.RoleBasicGrants(ctx, db, c.tenant, name); err != nil {
			return err
		}
		_, err = peripherals.ReplaceRoleBasicGrants(ctx, db, c.tenant, name, grants, by)
		return err
	}); err != nil {
		return nil, roleErr(ctx, err, name)
	}
	out, err := s.permissions(ctx, c.tenant, name)
	if err != nil {
		return nil, roleErr(ctx, err, name)
	}
	if added, removed := grantDiff(before, out.GetGrants()); len(added)+len(removed) > 0 {
		diff := map[string][2]any{}
		if len(added) > 0 {
			diff["granted"] = [2]any{nil, added}
		}
		if len(removed) > 0 {
			diff["revoked"] = [2]any{removed, nil}
		}
		s.d.recordFor(ctx, c, AuditEntityRoles, role.ID, audit.ActionUpdate, diff)
	}
	return out, nil
}

// grantDiff lists, as "entity action", what after adds to and drops from before.
func grantDiff(before []peripherals.RoleGrant, after []*RoleGrant) (added, removed []string) {
	was := map[string]bool{}
	for _, g := range before {
		was[grantKey(g)] = true
	}
	now := map[string]bool{}
	for _, g := range after {
		k := grantKey(peripherals.RoleGrant{Entity: g.GetEntity(), Action: g.GetAction()})
		now[k] = true
		if !was[k] {
			added = append(added, k)
		}
	}
	for k := range was {
		if !now[k] {
			removed = append(removed, k)
		}
	}
	slices.Sort(added)
	slices.Sort(removed)
	return added, removed
}

func (s *RoleAdminService) ListPermissionResources(ctx context.Context, _ *Empty) (*ListPermissionResourcesResponse, error) {
	if _, err := s.d.admin(ctx); err != nil {
		return nil, err
	}
	out := &ListPermissionResourcesResponse{Resources: []*PermissionResource{}}
	for _, r := range permission.Resources() {
		pr := &PermissionResource{Name: r.Name, Description: r.Description, Key: permission.ResourceKey(r.Name)}
		for _, a := range r.Actions {
			pr.Actions = append(pr.Actions, &PermissionResourceAction{Name: a.Name, Description: a.Description})
		}
		out.Resources = append(out.Resources, pr)
	}
	return out, nil
}
