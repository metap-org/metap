// Package tenants is registering, renaming, suspending and deleting tenants (roadmap 113 P3) as an
// administrative slice. Platform admins only: a tenant never administers tenants, itself included.
// types.go, rules.go (what a tenant's name and tier may be, the wire form), store.go (the registry and
// the first admin) with its Postgres adapter, and this file.
package tenants

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/metap-org/metap/internal/admin/kernel"
	"github.com/metap-org/metap/pkg/audit"
	"github.com/metap-org/metap/pkg/auth/mail"
	"github.com/metap-org/metap/pkg/kit/standard"
	"github.com/metap-org/metap/pkg/tenancy"
)

type Service struct {
	store  Store
	mailer Mailer
	deps   kernel.Deps
}

// New is the service over store. mailer sends the first admin's invitation (nil sends nothing, and the
// answer carries the link instead); deps carry the clock, the audit sink and the caller checks.
func New(store Store, mailer Mailer, deps kernel.Deps) *Service {
	return &Service{store: store, mailer: mailer, deps: deps}
}

func (s *Service) tenant(ctx context.Context, id uuid.UUID) (*Tenant, error) {
	d, err := s.store.Detail(ctx, id)
	if err != nil {
		return nil, kernel.TenantErr(err)
	}
	return s.viewOf(ctx, *d)
}

func (s *Service) viewOf(ctx context.Context, d tenancy.TenantDetail) (*Tenant, error) {
	members, err := s.store.MemberCount(ctx, d.ID)
	if err != nil {
		return nil, err
	}
	return view(d, members), nil
}

func (s *Service) List(ctx context.Context, req *ListTenantsRequest) (*ListTenantsResponse, error) {
	if _, err := s.deps.PlatformAdmin(ctx); err != nil {
		return nil, err
	}
	rows, total, err := s.store.Search(ctx, tenancy.TenantFilter{
		Query: req.GetQuery(), Status: req.GetStatus(), Tier: req.GetTier(), Limit: int(req.GetLimit()), Offset: int(req.GetOffset()),
	})
	if err != nil {
		return nil, err
	}
	out := make([]*Tenant, len(rows))
	for i, d := range rows {
		if out[i], err = s.viewOf(ctx, d); err != nil {
			return nil, err
		}
	}
	return &ListTenantsResponse{Tenants: out, Total: int32(total)}, nil
}

func (s *Service) Get(ctx context.Context, req *TenantIdRequest) (*Tenant, error) {
	if _, err := s.deps.PlatformAdmin(ctx); err != nil {
		return nil, err
	}
	id, err := kernel.ParseID(req.GetId())
	if err != nil {
		return nil, err
	}
	return s.tenant(ctx, id)
}

// Create registers a tenant on the shared tables and invites its first admin. The tenant is visible
// to the router only once the admin and the admin role exist; any failure on the way leaves the tenant
// deleted, never half-made.
func (s *Service) Create(ctx context.Context, req *CreateTenantRequest) (*CreateTenantResponse, error) {
	c, err := s.deps.PlatformAdmin(ctx)
	if err != nil {
		return nil, err
	}
	name, tier, err := newTenant(req.GetName(), req.GetTier())
	if err != nil {
		return nil, err
	}
	now := s.deps.Now()
	id := uuid.New()
	if err := s.store.Register(ctx, id, name, tier); err != nil {
		return nil, err
	}
	fail := func(cause error) error {
		_ = s.store.Deprovision(ctx, id)
		return cause
	}
	if req.GetProduct() != "" {
		if err := s.store.SetProduct(ctx, id, req.GetProduct()); err != nil {
			return nil, fail(err)
		}
	}
	// The admin, the roles, the invitation, the activation and the audit entry are one change: a
	// failure anywhere leaves none of them, and the registry row made above is then marked deleted.
	var made *Admin
	err = s.deps.Change(ctx, c, AuditEntity, id, audit.ActionCreate, func(ctx context.Context) (map[string][2]any, error) {
		var err error
		made, err = s.store.CreateAdmin(ctx, id, NewAdmin{Email: req.GetAdminEmail(), DisplayName: req.GetAdminDisplayName()}, c.SelfPtr(), now)
		if err != nil {
			return nil, kernel.IdentityErr(err)
		}
		return map[string][2]any{"name": {nil, name}, "tier": {nil, tier}, "adminEmail": {nil, made.Member.Email}}, nil
	})
	if err != nil {
		return nil, fail(err)
	}

	var link string
	var sendErr error = mail.ErrNotConfigured
	if s.mailer != nil {
		link = s.mailer.Link("/accept-invite", made.Token)
		sendErr = s.mailer.SendInvitation(ctx, mail.Invitation{
			To: made.Member.Email, TenantName: name, InviterName: s.deps.DisplayNameOf(ctx, c),
			Link: link, ExpiresAt: made.Invitation.ExpiresAt, NewAccount: made.NewAccount,
		})
	}
	if sendErr != nil && !errors.Is(sendErr, mail.ErrNotConfigured) {
		slog.Warn("tenant admin invitation email not sent", "tenant_id", id, standard.LogError, sendErr)
	}
	t, err := s.tenant(ctx, id)
	if err != nil {
		return nil, err
	}
	resp := &CreateTenantResponse{Tenant: t, AdminMemberId: made.Member.ID.String(),
		ExpiresAt: kernel.FormatTime(&made.Invitation.ExpiresAt), EmailSent: sendErr == nil}
	if sendErr != nil {
		resp.AcceptUrl = link
	}
	return resp, nil
}

// Update renames a tenant or changes its tier.
func (s *Service) Update(ctx context.Context, req *UpdateTenantRequest) (*Tenant, error) {
	c, err := s.deps.PlatformAdmin(ctx)
	if err != nil {
		return nil, err
	}
	id, err := kernel.ParseID(req.GetId())
	if err != nil {
		return nil, err
	}
	before, err := s.store.Detail(ctx, id)
	if err != nil {
		return nil, kernel.TenantErr(err)
	}
	name, tier, diff, err := patch(before, req.Name, req.Tier)
	if err != nil {
		return nil, err
	}
	err = s.deps.Change(ctx, c, AuditEntity, id, audit.ActionUpdate, func(ctx context.Context) (map[string][2]any, error) {
		if err := s.store.Update(ctx, id, name, tier); err != nil {
			return nil, err
		}
		if len(diff) == 0 {
			return nil, nil
		}
		return diff, nil
	})
	if err != nil {
		return nil, kernel.TenantErr(err)
	}
	return s.tenant(ctx, id)
}

// lifecycle runs one state change and returns the tenant afterwards.
func (s *Service) lifecycle(ctx context.Context, idStr, change string, fn func(ctx context.Context, id uuid.UUID, now time.Time) error,
	diff map[string][2]any) (*Tenant, error) {
	c, err := s.deps.PlatformAdmin(ctx)
	if err != nil {
		return nil, err
	}
	id, err := kernel.ParseID(idStr)
	if err != nil {
		return nil, err
	}
	diff["change"] = [2]any{nil, change}
	err = s.deps.Change(ctx, c, AuditEntity, id, audit.ActionUpdate, func(ctx context.Context) (map[string][2]any, error) {
		return diff, fn(ctx, id, s.deps.Now())
	})
	if err != nil {
		return nil, kernel.TenantErr(err)
	}
	return s.tenant(ctx, id)
}

func (s *Service) Suspend(ctx context.Context, req *TenantIdRequest) (*Tenant, error) {
	return s.lifecycle(ctx, req.GetId(), "suspend", func(ctx context.Context, id uuid.UUID, _ time.Time) error {
		return s.store.Suspend(ctx, id)
	}, map[string][2]any{"status": {"active", "suspended"}})
}

func (s *Service) Resume(ctx context.Context, req *TenantIdRequest) (*Tenant, error) {
	return s.lifecycle(ctx, req.GetId(), "resume", func(ctx context.Context, id uuid.UUID, _ time.Time) error {
		return s.store.Resume(ctx, id)
	}, map[string][2]any{"status": {"suspended", "active"}})
}

func (s *Service) RequestDeletion(ctx context.Context, req *RequestTenantDeletionRequest) (*Tenant, error) {
	retention := time.Duration(req.GetRetentionDays()) * 24 * time.Hour
	return s.lifecycle(ctx, req.GetId(), "request-deletion", func(ctx context.Context, id uuid.UUID, now time.Time) error {
		return s.store.RequestDeletion(ctx, id, now, retention)
	}, map[string][2]any{"status": {nil, "suspended"}, "deletionRequested": {false, true}})
}

func (s *Service) CancelDeletion(ctx context.Context, req *TenantIdRequest) (*Tenant, error) {
	return s.lifecycle(ctx, req.GetId(), "cancel-deletion", func(ctx context.Context, id uuid.UUID, now time.Time) error {
		return s.store.CancelDeletion(ctx, id, now)
	}, map[string][2]any{"deletionRequested": {true, false}})
}
