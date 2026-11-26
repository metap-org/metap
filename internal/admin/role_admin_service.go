// RoleAdminService (roadmap 114): a tenant's role catalog. Roles live next to user_roles, in the
// tenant's own database through Router; members holding a role are counted in the shared one.
package admin

import (
	"context"
	"errors"
	"fmt"
	"github.com/metap-org/metap/pkg/permission/rolestore"
	"net/http"
	"regexp"
	"strings"

	"github.com/google/uuid"

	"github.com/metap-org/metap/pkg/audit"
	"github.com/metap-org/metap/pkg/auth/identity"
	"github.com/metap-org/metap/pkg/crud"
	"github.com/metap-org/metap/pkg/kit/errcode"
)

// AuditEntityRoles is the audit-trail entity name role administration records under.
const AuditEntityRoles = "metadata.roles"

// roleNamePattern is what a new role may be called: it ends up in tokens' role lists, policy rows
// and URLs, so keep it to an identifier. Roles that predate the catalog keep whatever name they had.
var roleNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,79}$`)

// reservedRoleNames can't be created: the permission matrix uses __everyone__ for "no role
// restriction".
var reservedRoleNames = map[string]bool{"__everyone__": true}

// usageSampleSize caps how many holders/policies a role_in_use error lists by name.
const usageSampleSize = 5

// RoleAdminService manages the caller's tenant's roles. Every call needs the admin role.
type RoleAdminService struct {
	d identityDeps
}

func NewRoleAdminService(cfg IdentityConfig) *RoleAdminService {
	return &RoleAdminService{d: identityDeps{cfg: cfg}}
}

func roleErr(ctx context.Context, err error, name string) error {
	switch {
	case errors.Is(err, rolestore.ErrRoleNotFound):
		return svcErr(ctx, http.StatusNotFound, errcode.NotFound, "Role "+name+" not found.")
	case errors.Is(err, rolestore.ErrRoleExists):
		return sendServiceError(ctx, &crud.ServiceError{Status: http.StatusConflict, Code: errcode.RoleExists,
			Message: "A role named " + name + " already exists.", FieldErrors: map[string][]string{"name": {"already exists"}}})
	}
	return rpcError(ctx, err)
}

// activeHolders counts, per role, the active members holding it.
func (d identityDeps) activeHolders(ctx context.Context, tenant uuid.UUID, roles []string) (map[string]int, error) {
	holders := map[string][]uuid.UUID{}
	if err := d.withTenantTx(ctx, tenant, func(db rolestore.DBTX) error {
		for _, r := range roles {
			ids, err := rolestore.UsersWithRole(ctx, db, tenant, r)
			if err != nil {
				return err
			}
			holders[r] = ids
		}
		return nil
	}); err != nil {
		return nil, err
	}
	out := make(map[string]int, len(roles))
	for _, r := range roles {
		if len(holders[r]) == 0 {
			continue
		}
		_, n, err := identity.ListMembers(ctx, d.cfg.Shared, tenant, identity.ListQuery{
			IDs: holders[r], Status: identity.StatusActive, Limit: 1,
		})
		if err != nil {
			return nil, err
		}
		out[r] = n
	}
	return out, nil
}

func toPBRole(r rolestore.Role, users, policies int) *Role {
	return &Role{
		Id: r.ID.String(), Name: r.Name, Label: r.Label, Description: r.Description, Builtin: r.Builtin,
		UserCount: int32(users), PolicyCount: int32(policies),
		CreatedAt: formatTime(&r.CreatedAt), UpdatedAt: formatTime(&r.UpdatedAt),
	}
}

// loadRoles reads the catalog (creating the built-in rows on first use) with each role's usage.
func (s *RoleAdminService) loadRoles(ctx context.Context, tenant uuid.UUID, search string) ([]*Role, error) {
	var rows []rolestore.Role
	var policyCounts map[string]int
	if err := s.d.withTenantTx(ctx, tenant, func(db rolestore.DBTX) error {
		if err := rolestore.EnsureBuiltinRoles(ctx, db, tenant); err != nil {
			return err
		}
		var err error
		if rows, err = rolestore.ListRoleRows(ctx, db, tenant, search); err != nil {
			return err
		}
		policyCounts, err = rolestore.PolicyCountsByRole(ctx, db, tenant)
		return err
	}); err != nil {
		return nil, err
	}
	names := make([]string, len(rows))
	for i, r := range rows {
		names[i] = r.Name
	}
	users, err := s.d.activeHolders(ctx, tenant, names)
	if err != nil {
		return nil, err
	}
	out := make([]*Role, len(rows))
	for i, r := range rows {
		out[i] = toPBRole(r, users[r.Name], policyCounts[r.Name])
	}
	return out, nil
}

func (s *RoleAdminService) ListRoles(ctx context.Context, req *ListRolesRequest) (*ListRolesResponse, error) {
	c, err := s.d.admin(ctx)
	if err != nil {
		return nil, err
	}
	roles, err := s.loadRoles(ctx, c.Tenant, req.GetSearch())
	if err != nil {
		return nil, rpcError(ctx, err)
	}
	return &ListRolesResponse{Roles: roles}, nil
}

func (s *RoleAdminService) role(ctx context.Context, tenant uuid.UUID, name string) (*Role, error) {
	var r rolestore.Role
	var policies []rolestore.PolicyRef
	if err := s.d.withTenantTx(ctx, tenant, func(db rolestore.DBTX) error {
		if err := rolestore.EnsureBuiltinRoles(ctx, db, tenant); err != nil {
			return err
		}
		var err error
		if r, err = rolestore.GetRoleByName(ctx, db, tenant, name); err != nil {
			return err
		}
		policies, err = rolestore.PoliciesNamingRole(ctx, db, tenant, name)
		return err
	}); err != nil {
		return nil, err
	}
	users, err := s.d.activeHolders(ctx, tenant, []string{name})
	if err != nil {
		return nil, err
	}
	return toPBRole(r, users[name], len(policies)), nil
}

func (s *RoleAdminService) GetRole(ctx context.Context, req *RoleNameRequest) (*Role, error) {
	c, err := s.d.admin(ctx)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(req.GetName())
	r, err := s.role(ctx, c.Tenant, name)
	if err != nil {
		return nil, roleErr(ctx, err, name)
	}
	return r, nil
}

func (s *RoleAdminService) CreateRole(ctx context.Context, req *CreateRoleRequest) (*Role, error) {
	c, err := s.d.admin(ctx)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(req.GetName())
	if !roleNamePattern.MatchString(name) || reservedRoleNames[name] {
		return nil, sendServiceError(ctx, &crud.ServiceError{Status: http.StatusBadRequest, Code: errcode.ValidationFailed,
			Message:     "A role name is lowercase letters, digits and underscores, starting with a letter (at most 80).",
			FieldErrors: map[string][]string{"name": {"lowercase letters, digits and underscores, starting with a letter"}}})
	}
	label, description := strings.TrimSpace(req.GetLabel()), strings.TrimSpace(req.GetDescription())
	var by *uuid.UUID
	if c.Self != uuid.Nil {
		self := c.Self
		by = &self
	}
	copyFrom := strings.TrimSpace(req.GetCopyFrom())
	var created rolestore.Role
	var copied []rolestore.RoleGrant
	err = s.d.changeInTenantNew(ctx, c, AuditEntityRoles, audit.ActionCreate, func(ctx context.Context) (uuid.UUID, map[string][2]any, error) {
		err := s.d.withTenantTx(ctx, c.Tenant, func(db rolestore.DBTX) error {
			if copyFrom != "" {
				if _, err := rolestore.GetRoleByName(ctx, db, c.Tenant, copyFrom); err != nil {
					return err
				}
				var err error
				if copied, err = rolestore.RoleBasicGrants(ctx, db, c.Tenant, copyFrom); err != nil {
					return err
				}
			}
			var err error
			if created, err = rolestore.CreateRole(ctx, db, c.Tenant, name, label, description, by); err != nil {
				return err
			}
			if len(copied) > 0 {
				_, err = rolestore.ReplaceRoleBasicGrants(ctx, db, c.Tenant, name, copied, by)
			}
			return err
		})
		if err != nil {
			return uuid.Nil, nil, err
		}
		diff := map[string][2]any{"name": {nil, name}, "label": {nil, label}, "description": {nil, description}}
		if copyFrom != "" {
			diff["copiedFrom"] = [2]any{nil, copyFrom}
		}
		return created.ID, diff, nil
	})
	if err != nil {
		if copyFrom != "" && errors.Is(err, rolestore.ErrRoleNotFound) {
			return nil, roleErr(ctx, err, copyFrom)
		}
		return nil, roleErr(ctx, err, name)
	}
	return toPBRole(created, 0, len(copied)), nil
}

func (s *RoleAdminService) UpdateRole(ctx context.Context, req *UpdateRoleRequest) (*Role, error) {
	c, err := s.d.admin(ctx)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(req.GetName())
	label, description := strings.TrimSpace(req.GetLabel()), strings.TrimSpace(req.GetDescription())
	var before, after rolestore.Role
	err = s.d.changeInTenantNew(ctx, c, AuditEntityRoles, audit.ActionUpdate, func(ctx context.Context) (uuid.UUID, map[string][2]any, error) {
		if err := s.d.withTenantTx(ctx, c.Tenant, func(db rolestore.DBTX) error {
			if err := rolestore.EnsureBuiltinRoles(ctx, db, c.Tenant); err != nil {
				return err
			}
			var err error
			if before, err = rolestore.GetRoleByName(ctx, db, c.Tenant, name); err != nil {
				return err
			}
			after, err = rolestore.UpdateRole(ctx, db, c.Tenant, name, label, description)
			return err
		}); err != nil {
			return uuid.Nil, nil, err
		}
		diff := map[string][2]any{}
		if before.Label != after.Label {
			diff["label"] = [2]any{before.Label, after.Label}
		}
		if before.Description != after.Description {
			diff["description"] = [2]any{before.Description, after.Description}
		}
		if len(diff) == 0 {
			return after.ID, nil, nil
		}
		return after.ID, diff, nil
	})
	if err != nil {
		return nil, roleErr(ctx, err, name)
	}
	r, err := s.role(ctx, c.Tenant, name)
	if err != nil {
		return nil, roleErr(ctx, err, name)
	}
	return r, nil
}

// DeleteRole refuses while the role is in use: decided in roadmap 114 over cascading, so removing a
// role never silently takes access away from anyone. The error lists what blocks it.
func (s *RoleAdminService) DeleteRole(ctx context.Context, req *RoleNameRequest) (*Empty, error) {
	c, err := s.d.admin(ctx)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(req.GetName())
	var r rolestore.Role
	var holders []uuid.UUID
	var policies []rolestore.PolicyRef
	if err := s.d.withTenantTx(ctx, c.Tenant, func(db rolestore.DBTX) error {
		if err := rolestore.EnsureBuiltinRoles(ctx, db, c.Tenant); err != nil {
			return err
		}
		var err error
		if r, err = rolestore.GetRoleByName(ctx, db, c.Tenant, name); err != nil {
			return err
		}
		if holders, err = rolestore.UsersWithRole(ctx, db, c.Tenant, name); err != nil {
			return err
		}
		policies, err = rolestore.PoliciesNamingRole(ctx, db, c.Tenant, name)
		return err
	}); err != nil {
		return nil, roleErr(ctx, err, name)
	}
	if r.Builtin {
		return nil, svcErr(ctx, http.StatusConflict, errcode.RoleInUse, "Role "+name+" is built in and can't be deleted.")
	}
	if len(holders) > 0 || len(policies) > 0 {
		return nil, s.inUseError(ctx, c.Tenant, name, holders, policies)
	}
	err = s.d.changeInTenant(ctx, c, AuditEntityRoles, r.ID, audit.ActionDelete, func(ctx context.Context) (map[string][2]any, error) {
		err := s.d.withTenantTx(ctx, c.Tenant, func(db rolestore.DBTX) error {
			return rolestore.DeleteRole(ctx, db, c.Tenant, name)
		})
		return map[string][2]any{"name": {name, nil}, "label": {r.Label, nil}, "description": {r.Description, nil}}, err
	})
	if err != nil {
		return nil, roleErr(ctx, err, name)
	}
	return &Empty{}, nil
}

// inUseError is role_in_use with what blocks the delete: fieldErrors.members (emails) and
// fieldErrors.policies ("entity action"), each capped at usageSampleSize plus a "+N more" entry.
func (s *RoleAdminService) inUseError(ctx context.Context, tenant uuid.UUID, name string, holders []uuid.UUID, policies []rolestore.PolicyRef) error {
	fields := map[string][]string{}
	if len(holders) > 0 {
		sample := holders
		if len(sample) > usageSampleSize {
			sample = sample[:usageSampleSize]
		}
		ms, _, err := identity.ListMembers(ctx, s.d.cfg.Shared, tenant, identity.ListQuery{IDs: sample, Limit: usageSampleSize})
		if err != nil {
			return rpcError(ctx, err)
		}
		for _, m := range ms {
			fields["members"] = append(fields["members"], m.Email)
		}
		if len(holders) > len(ms) {
			fields["members"] = append(fields["members"], fmt.Sprintf("+%d", len(holders)-len(ms)))
		}
	}
	for i, p := range policies {
		if i == usageSampleSize {
			fields["policies"] = append(fields["policies"], fmt.Sprintf("+%d", len(policies)-usageSampleSize))
			break
		}
		fields["policies"] = append(fields["policies"], p.Entity+" "+p.Action)
	}
	return sendServiceError(ctx, &crud.ServiceError{Status: http.StatusConflict, Code: errcode.RoleInUse,
		Message: fmt.Sprintf("Role %s is still in use: %d member(s) hold it and %d polic(ies) name it.",
			name, len(holders), len(policies)),
		FieldErrors: fields})
}

func (s *RoleAdminService) ListRoleAudit(ctx context.Context, req *ListRoleAuditRequest) (*ListUserAuditResponse, error) {
	c, err := s.d.admin(ctx)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(req.GetName())
	var r rolestore.Role
	if err := s.d.withTenantTx(ctx, c.Tenant, func(db rolestore.DBTX) error {
		var err error
		r, err = rolestore.GetRoleByName(ctx, db, c.Tenant, name)
		return err
	}); err != nil {
		return nil, roleErr(ctx, err, name)
	}
	return s.d.listAudit(ctx, c, AuditEntityRoles, r.ID, req.GetCursor(), req.GetLimit())
}
