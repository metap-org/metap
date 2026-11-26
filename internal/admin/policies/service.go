// Package policies is a tenant's entity policies (who may read, create, update, delete or transition
// which entity), the permission matrix's save call, explaining a decision, and dropping a member's
// cached context attributes, as an administrative slice: types.go, rules.go (what a policy request
// may hold, the wire form, the audit view), store.go with its adapter over PermissionService, and this
// file. It needs no identity database, so a service that does not issue tokens serves it too. Every
// call needs the admin role and every change is audited: who may do what is the most
// security-relevant thing an admin edits.
package policies

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"

	"github.com/metap-org/metap/internal/admin/kernel"
	"github.com/metap-org/metap/pkg/audit"
	"github.com/metap-org/metap/pkg/permission"
)

type Service struct {
	store Store
	cache ContextInvalidator
	deps  kernel.Deps
}

// New is the service over store. cache (may be nil) is what InvalidateContext drops from; deps carry the
// audit store and, when set, the sink that makes a change and its entry one transaction.
func New(store Store, cache ContextInvalidator, deps kernel.Deps) *Service {
	return &Service{store: store, cache: cache, deps: deps}
}

func (s *Service) List(ctx context.Context, req *ListPoliciesRequest) (*PolicyList, error) {
	c, err := s.deps.Admin(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.store.List(ctx, c.Tenant, req.Entity, req.Entity != "")
	if err != nil {
		return nil, err
	}
	return &PolicyList{Data: views(rows)}, nil
}

func (s *Service) Create(ctx context.Context, req *CreatePolicyRequest) (*PolicyOne, error) {
	c, err := s.deps.Admin(ctx)
	if err != nil {
		return nil, err
	}
	in, err := newPolicy(req, optionalUUID(c.RC.UserID))
	if err != nil {
		return nil, err
	}
	var row permission.PolicyRow
	err = s.deps.ChangeInTenantNew(ctx, c, AuditEntity, audit.ActionCreate, func(ctx context.Context) (uuid.UUID, map[string][2]any, error) {
		var err error
		if row, err = s.store.Create(ctx, c.Tenant, in); err != nil {
			return uuid.Nil, nil, err
		}
		return row.ID, fieldsOf(row, false), nil
	})
	if err != nil {
		return nil, err
	}
	return &PolicyOne{Data: view(row)}, nil
}

// Seed creates one context-subject, no-condition (pure RBAC) policy per action for the roles on the
// entity: the ergonomic counterpart to Create now that PermissionService denies by default when an
// entity/action has no policy at all.
func (s *Service) Seed(ctx context.Context, req *SeedDefaultPoliciesRequest) (*PolicyList, error) {
	c, err := s.deps.Admin(ctx)
	if err != nil {
		return nil, err
	}
	actions, err := seedActions(req)
	if err != nil {
		return nil, err
	}
	by := optionalUUID(c.RC.UserID)
	created := make([]*Policy, 0, len(actions))
	for _, act := range actions {
		in := NewPolicy{Entity: req.Entity, Action: act, Roles: req.Roles, HasRoles: true, By: by,
			Subject: permission.SubjectContext, Effect: permission.EffectAllow}
		var row permission.PolicyRow
		err := s.deps.ChangeInTenantNew(ctx, c, AuditEntity, audit.ActionCreate, func(ctx context.Context) (uuid.UUID, map[string][2]any, error) {
			var err error
			if row, err = s.store.Create(ctx, c.Tenant, in); err != nil {
				return uuid.Nil, nil, err
			}
			return row.ID, fieldsOf(row, false), nil
		})
		if err != nil {
			return nil, err
		}
		created = append(created, view(row))
	}
	return &PolicyList{Data: created}, nil
}

// SyncMatrix is the permission matrix's single save call: it replaces every basic-shaped policy of the
// entity with exactly the grants, atomically. It never touches an advanced policy (condition, field,
// record subject, deny).
func (s *Service) SyncMatrix(ctx context.Context, req *SyncMatrixRequest) (*PolicyList, error) {
	c, err := s.deps.Admin(ctx)
	if err != nil {
		return nil, err
	}
	grants, err := matrixGrants(req.Grants)
	if err != nil {
		return nil, err
	}
	// What the matrix held before, for the audit entry: read ahead of the replace.
	var before []string
	if old, err := s.store.List(ctx, c.Tenant, req.Entity, true); err == nil {
		before = basicGrants(old)
	}
	var rows []permission.PolicyRow
	err = s.deps.ChangeInTenantNew(ctx, c, AuditEntity, audit.ActionUpdate, func(ctx context.Context) (uuid.UUID, map[string][2]any, error) {
		var err error
		if rows, err = s.store.Sync(ctx, c.Tenant, req.Entity, grants, optionalUUID(c.RC.UserID)); err != nil {
			return uuid.Nil, nil, err
		}
		return kernel.NamedID("policies:"+c.Tenant.String(), req.Entity),
			map[string][2]any{"entity": {nil, req.Entity}, "grants": {before, basicGrants(rows)}}, nil
	})
	if err != nil {
		return nil, err
	}
	return &PolicyList{Data: views(rows)}, nil
}

func (s *Service) Delete(ctx context.Context, req *PolicyIDRequest) (*kernel.Empty, error) {
	c, err := s.deps.Admin(ctx)
	if err != nil {
		return nil, err
	}
	id, err := kernel.ParseID(req.ID)
	if err != nil {
		return nil, validationErr("`id` must be a UUID.")
	}
	// The policy as it was, for the audit entry: it is gone once deleted.
	var gone *permission.PolicyRow
	if all, err := s.store.List(ctx, c.Tenant, "", false); err == nil {
		for i := range all {
			if all[i].ID == id {
				gone = &all[i]
				break
			}
		}
	}
	err = s.deps.ChangeInTenantNew(ctx, c, AuditEntity, audit.ActionDelete, func(ctx context.Context) (uuid.UUID, map[string][2]any, error) {
		if err := s.store.Delete(ctx, c.Tenant, id); err != nil {
			return uuid.Nil, nil, err
		}
		if gone == nil {
			return id, nil, nil
		}
		return id, fieldsOf(*gone, true), nil
	})
	if err != nil {
		return nil, err
	}
	return &kernel.Empty{}, nil
}

// Explain says which policies decide an action on an entity for the caller.
func (s *Service) Explain(ctx context.Context, req *ExplainPolicyRequest) (*ExplainResponse, error) {
	c, err := s.deps.Admin(ctx)
	if err != nil {
		return nil, err
	}
	field := ""
	if req.Field != nil {
		field = *req.Field
	}
	explanation, err := s.store.Explain(ctx, c.RC, req.Entity, req.Action, field, req.Field != nil,
		permission.JsonObject(req.Record), req.Record != nil)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(explanation)
	if err != nil {
		return nil, err
	}
	return &ExplainResponse{Data: raw}, nil
}

// InvalidateContext drops a member's cached context attributes.
func (s *Service) InvalidateContext(ctx context.Context, req *UserIDRequest) (*kernel.Empty, error) {
	c, err := s.deps.Admin(ctx)
	if err != nil {
		return nil, err
	}
	userID, err := kernel.ParseID(req.UserID)
	if err != nil {
		return nil, validationErr("`userId` must be a UUID.")
	}
	if s.cache != nil {
		s.cache.Invalidate(c.Tenant, userID)
	}
	return &kernel.Empty{}, nil
}
