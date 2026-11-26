// Package account is the signed-in person's own view (roadmap 113): who they are, their display name,
// the tenants they belong to and the people they can pick in their own tenant. Any signed-in subject
// may call it; nothing here administers anyone else. types.go, store.go with its Postgres adapter, and
// this file. (Their two-step settings are internal/admin/mfa and their sessions internal/admin/sessions.)
package account

import (
	"context"
	"errors"

	"github.com/metap-org/metap/internal/admin/kernel"
	"github.com/metap-org/metap/pkg/auth/identity"
)

type Service struct {
	store Store
	deps  kernel.Deps
}

func New(store Store, deps kernel.Deps) *Service { return &Service{store: store, deps: deps} }

func (s *Service) me(ctx context.Context, c kernel.Caller) (*Me, error) {
	me := &Me{UserId: c.RC.UserID, TenantId: c.RC.TenantID, Roles: c.RC.Roles}
	if me.Roles == nil {
		me.Roles = []string{}
	}
	m, err := s.store.Member(ctx, c.Tenant, c.Self)
	if err == nil {
		me.Email, me.DisplayName = m.Email, m.DisplayName
	} else if !errors.Is(err, identity.ErrNotFound) {
		return nil, err
	}
	return me, nil
}

func (s *Service) Get(ctx context.Context, _ *kernel.Empty) (*Me, error) {
	c, err := s.deps.Caller(ctx)
	if err != nil {
		return nil, err
	}
	return s.me(ctx, c)
}

func (s *Service) UpdateProfile(ctx context.Context, req *UpdateProfileRequest) (*Me, error) {
	c, err := s.deps.Caller(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.store.Rename(ctx, c.Tenant, c.Self, req.GetDisplayName()); err != nil {
		return nil, kernel.IdentityErr(err)
	}
	return s.me(ctx, c)
}

func (s *Service) ListMyTenants(ctx context.Context, _ *kernel.Empty) (*ListMyTenantsResponse, error) {
	c, err := s.deps.Caller(ctx)
	if err != nil {
		return nil, err
	}
	person, err := s.store.IdentityOf(ctx, c.Tenant, c.Self)
	if errors.Is(err, identity.ErrNotFound) {
		return &ListMyTenantsResponse{Tenants: []*MyTenant{}}, nil
	}
	if err != nil {
		return nil, err
	}
	ms, err := s.store.Memberships(ctx, person)
	if err != nil {
		return nil, err
	}
	out := make([]*MyTenant, len(ms))
	for i, m := range ms {
		out[i] = &MyTenant{TenantId: m.TenantID.String(), Name: m.TenantName, Current: m.TenantID == c.Tenant}
	}
	return &ListMyTenantsResponse{Tenants: out}, nil
}

// ListTenantUsers is the active members of the caller's tenant (for pickers), up to req.Limit.
func (s *Service) ListTenantUsers(ctx context.Context, req *ListTenantUsersRequest) (*ListTenantUsersResponse, error) {
	c, err := s.deps.Caller(ctx)
	if err != nil {
		return nil, err
	}
	out := &ListTenantUsersResponse{Users: []*TenantUser{}}
	want := int(req.GetLimit())
	for offset := 0; ; offset += identity.MaxListLimit {
		page, total, err := s.store.Members(ctx, c.Tenant, identity.ListQuery{
			Search: req.GetSearch(), Status: identity.StatusActive, Offset: offset, Limit: identity.MaxListLimit,
		})
		if err != nil {
			return nil, err
		}
		for _, m := range page {
			if want > 0 && len(out.Users) == want {
				return out, nil
			}
			out.Users = append(out.Users, &TenantUser{Id: m.ID.String(), Email: m.Email, DisplayName: m.DisplayName})
		}
		if len(page) == 0 || offset+len(page) >= total {
			return out, nil
		}
	}
}
