// Package roles is a tenant's role catalog (roadmap 114) and what each role may do (roadmap 114 P3),
// as an administrative slice: types.go, rules.go (names, grants, the wire form), store.go with its
// Postgres adapter, and this file. A saved grant is written straight to the policy table, so a
// service holding its policies in memory sees it within POLICY_CACHE_TTL_SECONDS, not on the next
// request. Every call needs the admin role.
package roles

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/metap-org/metap/internal/admin/kernel"
	"github.com/metap-org/metap/pkg/audit"
	"github.com/metap-org/metap/pkg/kit/errcode"
	"github.com/metap-org/metap/pkg/permission"
	"github.com/metap-org/metap/pkg/permission/rolestore"
)

type Service struct {
	store Store
	deps  kernel.Deps
}

func New(store Store, deps kernel.Deps) *Service { return &Service{store: store, deps: deps} }

func (s *Service) List(ctx context.Context, req *ListRolesRequest) (*ListRolesResponse, error) {
	c, err := s.deps.Admin(ctx)
	if err != nil {
		return nil, err
	}
	rows, policyCounts, err := s.store.Catalog(ctx, c.Tenant, req.GetSearch())
	if err != nil {
		return nil, err
	}
	names := make([]string, len(rows))
	for i, r := range rows {
		names[i] = r.Name
	}
	users, err := s.store.ActiveHolders(ctx, c.Tenant, names)
	if err != nil {
		return nil, err
	}
	out := make([]*Role, len(rows))
	for i, r := range rows {
		out[i] = view(r, users[r.Name], policyCounts[r.Name])
	}
	return &ListRolesResponse{Roles: out}, nil
}

func (s *Service) role(ctx context.Context, tenant uuid.UUID, name string) (*Role, error) {
	r, policies, err := s.store.Get(ctx, tenant, name)
	if err != nil {
		return nil, storeErr(err, name)
	}
	users, err := s.store.ActiveHolders(ctx, tenant, []string{name})
	if err != nil {
		return nil, err
	}
	return view(r, users[name], len(policies)), nil
}

func (s *Service) Get(ctx context.Context, req *RoleNameRequest) (*Role, error) {
	c, err := s.deps.Admin(ctx)
	if err != nil {
		return nil, err
	}
	return s.role(ctx, c.Tenant, strings.TrimSpace(req.GetName()))
}

func (s *Service) Create(ctx context.Context, req *CreateRoleRequest) (*Role, error) {
	c, err := s.deps.Admin(ctx)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(req.GetName())
	if err := checkName(name); err != nil {
		return nil, err
	}
	label, description := strings.TrimSpace(req.GetLabel()), strings.TrimSpace(req.GetDescription())
	copyFrom := strings.TrimSpace(req.GetCopyFrom())
	var created rolestore.Role
	var copied int
	err = s.deps.ChangeInTenantNew(ctx, c, AuditEntity, audit.ActionCreate, func(ctx context.Context) (uuid.UUID, map[string][2]any, error) {
		var err error
		created, copied, err = s.store.Create(ctx, c.Tenant, name, label, description, copyFrom, c.SelfPtr())
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
			return nil, storeErr(err, copyFrom)
		}
		return nil, storeErr(err, name)
	}
	return view(created, 0, copied), nil
}

func (s *Service) Update(ctx context.Context, req *UpdateRoleRequest) (*Role, error) {
	c, err := s.deps.Admin(ctx)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(req.GetName())
	label, description := strings.TrimSpace(req.GetLabel()), strings.TrimSpace(req.GetDescription())
	err = s.deps.ChangeInTenantNew(ctx, c, AuditEntity, audit.ActionUpdate, func(ctx context.Context) (uuid.UUID, map[string][2]any, error) {
		before, after, err := s.store.Update(ctx, c.Tenant, name, label, description)
		if err != nil {
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
		return nil, storeErr(err, name)
	}
	return s.role(ctx, c.Tenant, name)
}

// Delete refuses while the role is in use: decided in roadmap 114 over cascading, so removing a role
// never silently takes access away from anyone. The error lists what blocks it.
func (s *Service) Delete(ctx context.Context, req *RoleNameRequest) (*kernel.Empty, error) {
	c, err := s.deps.Admin(ctx)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(req.GetName())
	r, holders, policies, err := s.store.Usage(ctx, c.Tenant, name)
	if err != nil {
		return nil, storeErr(err, name)
	}
	if r.Builtin {
		return nil, kernel.Err(http.StatusConflict, errcode.RoleInUse, "Role "+name+" is built in and can't be deleted.")
	}
	if len(holders) > 0 || len(policies) > 0 {
		sample := holders
		if len(sample) > usageSampleSize {
			sample = sample[:usageSampleSize]
		}
		var emails []string
		if len(sample) > 0 {
			if emails, err = s.store.HolderEmails(ctx, c.Tenant, sample, usageSampleSize); err != nil {
				return nil, err
			}
		}
		return nil, inUseError(name, len(holders), emails, policies)
	}
	err = s.deps.ChangeInTenant(ctx, c, AuditEntity, r.ID, audit.ActionDelete, func(ctx context.Context) (map[string][2]any, error) {
		err := s.store.Delete(ctx, c.Tenant, name)
		return map[string][2]any{"name": {name, nil}, "label": {r.Label, nil}, "description": {r.Description, nil}}, err
	})
	if err != nil {
		return nil, storeErr(err, name)
	}
	return &kernel.Empty{}, nil
}

func (s *Service) ListAudit(ctx context.Context, req *ListRoleAuditRequest) (*kernel.ListUserAuditResponse, error) {
	c, err := s.deps.Admin(ctx)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(req.GetName())
	r, err := s.store.Role(ctx, c.Tenant, name)
	if err != nil {
		return nil, storeErr(err, name)
	}
	return s.deps.ListAudit(ctx, c, AuditEntity, r.ID, req.GetCursor(), req.GetLimit())
}

func (s *Service) permissions(ctx context.Context, tenant uuid.UUID, name string) (*RolePermissions, error) {
	grants, advanced, err := s.store.Permissions(ctx, tenant, name)
	if err != nil {
		return nil, err
	}
	return permissionsView(name, grants, advanced), nil
}

func (s *Service) GetPermissions(ctx context.Context, req *RoleNameRequest) (*RolePermissions, error) {
	c, err := s.deps.Admin(ctx)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(req.GetName())
	out, err := s.permissions(ctx, c.Tenant, name)
	if err != nil {
		return nil, storeErr(err, name)
	}
	return out, nil
}

// SetPermissions replaces the role's basic grants as one change with its audit entry.
func (s *Service) SetPermissions(ctx context.Context, req *SetRolePermissionsRequest) (*RolePermissions, error) {
	c, err := s.deps.Admin(ctx)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(req.GetRole())
	grants, err := checkGrants(name, req.GetGrants())
	if err != nil {
		return nil, err
	}
	var out *RolePermissions
	err = s.deps.ChangeInTenantNew(ctx, c, AuditEntity, audit.ActionUpdate, func(ctx context.Context) (uuid.UUID, map[string][2]any, error) {
		role, before, err := s.store.ReplaceGrants(ctx, c.Tenant, name, grants, c.SelfPtr())
		if err != nil {
			return uuid.Nil, nil, err
		}
		var after []rolestore.RoleGrant
		var advanced []rolestore.AdvancedPolicy
		if after, advanced, err = s.store.Permissions(ctx, c.Tenant, name); err != nil {
			return uuid.Nil, nil, err
		}
		out = permissionsView(name, after, advanced)
		added, removed := grantDiff(before, after)
		if len(added)+len(removed) == 0 {
			return role.ID, nil, nil
		}
		diff := map[string][2]any{}
		if len(added) > 0 {
			diff["granted"] = [2]any{nil, added}
		}
		if len(removed) > 0 {
			diff["revoked"] = [2]any{removed, nil}
		}
		return role.ID, diff, nil
	})
	if err != nil {
		return nil, storeErr(err, name)
	}
	return out, nil
}

// ListResources is what an admin UI can grant besides entity actions: the virtual resources a
// product declares (roadmap 115 M6).
func (s *Service) ListResources(ctx context.Context, _ *kernel.Empty) (*ListPermissionResourcesResponse, error) {
	if _, err := s.deps.Admin(ctx); err != nil {
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
