package support

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/metap-org/metap/internal/admin/kernel"
	"github.com/metap-org/metap/pkg/auth/identity"
	"github.com/metap-org/metap/pkg/permission/rolestore"
	"github.com/metap-org/metap/pkg/tenancy"
)

// PgStore keeps grants in the shared identity database and roles in each tenant's own.
type PgStore struct {
	deps     kernel.Deps
	registry *tenancy.PostgresTenantRegistry
}

func NewPgStore(deps kernel.Deps) *PgStore {
	return &PgStore{deps: deps, registry: tenancy.NewPostgresTenantRegistry(deps.Cfg.Shared)}
}

func (s *PgStore) db() kernel.IdentityDB { return s.deps.DB() }

func (s *PgStore) CreateGrant(ctx context.Context, tenant, by uuid.UUID, reason, role string, hours int, now time.Time) (*identity.SupportGrant, error) {
	return identity.CreateSupportGrant(ctx, s.db(), tenant, by, reason, role, hours, now)
}

func (s *PgStore) ListGrants(ctx context.Context, tenant uuid.UUID) ([]identity.SupportGrant, error) {
	return identity.ListSupportGrants(ctx, s.db(), tenant)
}

func (s *PgStore) GetGrant(ctx context.Context, tenant, id uuid.UUID) (*identity.SupportGrant, error) {
	return identity.GetSupportGrant(ctx, s.db(), tenant, id)
}

func (s *PgStore) LatestActiveGrant(ctx context.Context, tenant uuid.UUID, now time.Time) (*identity.SupportGrant, error) {
	return identity.LatestActiveSupportGrant(ctx, s.db(), tenant, now)
}

func (s *PgStore) RevokeGrant(ctx context.Context, tenant, id uuid.UUID, now time.Time) ([]uuid.UUID, error) {
	return identity.RevokeSupportGrant(ctx, s.db(), tenant, id, now)
}

func (s *PgStore) ExpireSessions(ctx context.Context, tenant uuid.UUID, now time.Time) ([]uuid.UUID, error) {
	return identity.ExpireSupportSessions(ctx, s.db(), tenant, now)
}

func (s *PgStore) TenantReachable(ctx context.Context, tenant uuid.UUID) error {
	_, err := s.registry.Get(ctx, tenant)
	return err
}

func (s *PgStore) Person(ctx context.Context, tenant, id uuid.UUID) (*identity.Member, error) {
	return identity.GetMember(ctx, s.db(), tenant, id)
}

func (s *PgStore) CreateAccount(ctx context.Context, tenant uuid.UUID, name string) (*identity.Member, error) {
	return identity.CreateServiceAccount(ctx, s.db(), tenant, name, nil)
}

func (s *PgStore) RemoveMember(ctx context.Context, tenant, id uuid.UUID, now time.Time) error {
	return identity.RemoveMember(ctx, s.db(), tenant, id, now)
}

func (s *PgStore) CreateKey(ctx context.Context, tenant, member uuid.UUID, name string, expires, now time.Time) (string, *identity.APIKey, error) {
	return identity.CreateAPIKey(ctx, s.db(), tenant, member, name, &expires, nil, now)
}

func (s *PgStore) RecordSession(ctx context.Context, tenant uuid.UUID, ss identity.SupportSession) error {
	return identity.RecordSupportSession(ctx, s.db(), ss, tenant)
}

func (s *PgStore) KnownRoles(ctx context.Context, tenant uuid.UUID) ([]string, error) {
	return s.deps.KnownRoles(ctx, tenant)
}

func (s *PgStore) AssignRole(ctx context.Context, tenant, member uuid.UUID, role string) error {
	return s.deps.WithTenantTx(ctx, tenant, func(db rolestore.DBTX) error {
		return rolestore.AssignRole(ctx, db, tenant, member, role, nil)
	})
}

func (s *PgStore) ClearRoles(ctx context.Context, tenant uuid.UUID, members []uuid.UUID) error {
	return s.deps.WithTenantTx(ctx, tenant, func(db rolestore.DBTX) error {
		for _, m := range members {
			if err := rolestore.RevokeAllRoles(ctx, db, tenant, m); err != nil {
				return err
			}
		}
		return nil
	})
}
