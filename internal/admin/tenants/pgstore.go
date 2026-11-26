package tenants

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/metap-org/metap/internal/admin/kernel"
	"github.com/metap-org/metap/pkg/auth/identity"
	"github.com/metap-org/metap/pkg/permission"
	"github.com/metap-org/metap/pkg/permission/rolestore"
	"github.com/metap-org/metap/pkg/tenancy"
)

// PgStore keeps tenants in the control registry on the shared database.
type PgStore struct {
	deps     kernel.Deps
	registry *tenancy.PostgresTenantRegistry
}

func NewPgStore(deps kernel.Deps) *PgStore {
	return &PgStore{deps: deps, registry: tenancy.NewPostgresTenantRegistry(deps.Cfg.Shared)}
}

func (s *PgStore) db() kernel.IdentityDB               { return s.deps.DB() }
func (s *PgStore) in() *tenancy.PostgresTenantRegistry { return s.registry.In(s.db()) }

func (s *PgStore) Detail(ctx context.Context, id uuid.UUID) (*tenancy.TenantDetail, error) {
	return s.registry.GetDetail(ctx, id)
}

func (s *PgStore) Search(ctx context.Context, f tenancy.TenantFilter) ([]tenancy.TenantDetail, int, error) {
	return s.registry.Search(ctx, f)
}

func (s *PgStore) MemberCount(ctx context.Context, id uuid.UUID) (int32, error) {
	var n int32
	err := s.deps.Cfg.Shared.QueryRow(ctx,
		`SELECT count(*) FROM metadata.users WHERE tenant_id = $1 AND deleted_at IS NULL`, id).Scan(&n)
	return n, err
}

func (s *PgStore) Register(ctx context.Context, id uuid.UUID, name, tier string) error {
	return s.registry.RegisterShared(ctx, id, name, tier, "provisioning")
}

func (s *PgStore) Deprovision(ctx context.Context, id uuid.UUID) error {
	_, err := s.registry.Deprovision(ctx, id)
	return err
}

func (s *PgStore) SetProduct(ctx context.Context, id uuid.UUID, product string) error {
	_, err := s.registry.SetProduct(ctx, id, product)
	return err
}

func (s *PgStore) CreateAdmin(ctx context.Context, tenant uuid.UUID, in NewAdmin, invitedBy *uuid.UUID, now time.Time) (*Admin, error) {
	m, _, err := identity.AddMember(ctx, s.db(), identity.NewMember{
		TenantID: tenant, Email: in.Email, DisplayName: in.DisplayName, InvitedBy: invitedBy, Status: identity.StatusInvited})
	if err != nil {
		return nil, err
	}
	if err := rolestore.EnsureBuiltinRoles(ctx, s.db(), tenant); err != nil {
		return nil, err
	}
	if err := rolestore.AssignRole(ctx, s.db(), tenant, m.ID, permission.RoleAdmin, invitedBy); err != nil {
		return nil, err
	}
	newAccount, err := identity.CanSetPasswordOnAccept(ctx, s.db(), m.IdentityID, m.ID)
	if err != nil {
		return nil, err
	}
	token, inv, err := identity.CreateInvitation(ctx, s.db(), tenant, m.ID, m.IdentityID, newAccount, invitedBy, now)
	if err != nil {
		return nil, err
	}
	if ok, err := s.in().SetStatus(ctx, tenant, "active"); err != nil || !ok {
		return nil, errors.Join(err, errors.New("could not activate the tenant"))
	}
	return &Admin{Member: m, Token: token, Invitation: inv, NewAccount: newAccount}, nil
}

func (s *PgStore) Update(ctx context.Context, id uuid.UUID, name, tier *string) error {
	return s.in().Update(ctx, id, name, tier)
}
func (s *PgStore) Suspend(ctx context.Context, id uuid.UUID) error { return s.in().Suspend(ctx, id) }
func (s *PgStore) Resume(ctx context.Context, id uuid.UUID) error  { return s.in().Resume(ctx, id) }
func (s *PgStore) RequestDeletion(ctx context.Context, id uuid.UUID, now time.Time, retention time.Duration) error {
	return s.in().RequestDeletion(ctx, id, now, retention)
}
func (s *PgStore) CancelDeletion(ctx context.Context, id uuid.UUID, now time.Time) error {
	return s.in().CancelDeletion(ctx, id, now)
}
