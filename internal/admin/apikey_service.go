package admin

import (
	"context"
	"github.com/metap-org/metap/pkg/permission/rolestore"
	"net/http"

	"github.com/google/uuid"

	"github.com/metap-org/metap/pkg/audit"
	"github.com/metap-org/metap/pkg/auth/identity"
	"github.com/metap-org/metap/pkg/kit/errcode"
)

func (s *UserAdminService) CreateServiceAccount(ctx context.Context, req *CreateServiceAccountRequest) (*User, error) {
	c, err := adminDurable(s.d, ctx)
	if err != nil {
		return nil, err
	}
	roles := dedupe(req.GetRoles())
	if err := s.d.checkRoles(ctx, c.Tenant, roles); err != nil {
		return nil, err
	}
	var createdBy *uuid.UUID
	if c.Self != uuid.Nil {
		createdBy = &c.Self
	}
	m, err := identity.CreateServiceAccount(ctx, s.d.cfg.Shared, c.Tenant, req.GetName(), createdBy)
	if err != nil {
		return nil, svcErr(ctx, http.StatusBadRequest, errcode.ValidationFailed, err.Error())
	}
	if len(roles) > 0 {
		if err := s.d.withTenantTx(ctx, c.Tenant, func(db rolestore.DBTX) error {
			for _, r := range roles {
				if err := rolestore.AssignRole(ctx, db, c.Tenant, m.ID, r, createdBy); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			_ = identity.RemoveMember(ctx, s.d.cfg.Shared, c.Tenant, m.ID, s.d.now())
			return nil, rpcError(ctx, err)
		}
	}
	s.d.record(ctx, c, m.ID, audit.ActionCreate, map[string][2]any{
		"serviceAccount": {nil, m.DisplayName}, "roles": {nil, roles},
	})
	u, err := s.d.user(ctx, c.Tenant, m.ID)
	if err != nil {
		return nil, identityErr(ctx, err)
	}
	return u, nil
}

func (s *UserAdminService) ListServiceAccounts(ctx context.Context, _ *Empty) (*ListUsersResponse, error) {
	c, err := s.admin(ctx)
	if err != nil {
		return nil, err
	}
	ms, err := identity.ListServiceAccounts(ctx, s.d.cfg.Shared, c.Tenant)
	if err != nil {
		return nil, identityErr(ctx, err)
	}
	users, err := s.d.toUsers(ctx, c.Tenant, ms)
	if err != nil {
		return nil, identityErr(ctx, err)
	}
	return &ListUsersResponse{Users: users, Total: int32(len(users))}, nil
}
