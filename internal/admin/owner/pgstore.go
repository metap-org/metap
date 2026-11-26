package owner

import (
	"context"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/metap-org/metap/internal/admin/kernel"
	"github.com/metap-org/metap/pkg/auth/identity"
	"github.com/metap-org/metap/pkg/permission"
	"github.com/metap-org/metap/pkg/permission/rolestore"
	"github.com/metap-org/metap/pkg/tenancy"
)

// PgStore keeps ownership on the shared database.
type PgStore struct{ deps kernel.Deps }

func NewPgStore(deps kernel.Deps) *PgStore { return &PgStore{deps: deps} }

func (s *PgStore) db() kernel.IdentityDB { return s.deps.DB() }

func (s *PgStore) OwnerID(ctx context.Context, t uuid.UUID) (uuid.UUID, error) {
	return identity.OwnerID(ctx, s.db(), t)
}
func (s *PgStore) SetOwner(ctx context.Context, t, m uuid.UUID) error {
	return identity.SetOwner(ctx, s.db(), t, m)
}

func (s *PgStore) ActiveAdmins(ctx context.Context, tenant uuid.UUID) ([]identity.Member, error) {
	var ids []uuid.UUID
	if err := s.deps.WithTenantTx(ctx, tenant, func(db rolestore.DBTX) error {
		var err error
		ids, err = rolestore.UsersWithRole(ctx, db, tenant, permission.RoleAdmin)
		return err
	}); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}
	ms, _, err := identity.ListMembers(ctx, s.db(), tenant, identity.ListQuery{IDs: ids, Status: identity.StatusActive, Limit: 200})
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(ms, func(m identity.Member) bool { return m.AuthProvider == identity.ServiceAccountProvider }), nil
}

func (s *PgStore) ActivePeople(ctx context.Context, tenant uuid.UUID) ([]identity.Member, error) {
	ms, _, err := identity.ListMembers(ctx, s.deps.Cfg.Shared, tenant, identity.ListQuery{Status: identity.StatusActive, Limit: identity.MaxListLimit})
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(ms, func(m identity.Member) bool { return m.AuthProvider == identity.ServiceAccountProvider }), nil
}

func (s *PgStore) Member(ctx context.Context, t, id uuid.UUID) (*identity.Member, error) {
	return identity.GetMember(ctx, s.db(), t, id)
}

func (s *PgStore) TenantName(ctx context.Context, tenant uuid.UUID) string {
	var name string
	_ = s.deps.Cfg.Shared.QueryRow(ctx, `SELECT name FROM control.tenants WHERE id = $1`, tenant).Scan(&name)
	return name
}

func (s *PgStore) PendingRecovery(ctx context.Context, t uuid.UUID) (*identity.OwnerRecovery, error) {
	return identity.PendingOwnerRecovery(ctx, s.db(), t)
}
func (s *PgStore) RequestRecovery(ctx context.Context, t, newOwner, by uuid.UUID, byEmail, reason string, now time.Time) (*identity.OwnerRecovery, error) {
	return identity.RequestOwnerRecovery(ctx, s.db(), t, newOwner, by, byEmail, reason, now)
}
func (s *PgStore) CancelRecovery(ctx context.Context, t, by uuid.UUID, now time.Time) (*identity.OwnerRecovery, error) {
	return identity.CancelOwnerRecovery(ctx, s.db(), t, by, now)
}
func (s *PgStore) CompleteRecovery(ctx context.Context, t uuid.UUID, now time.Time) (*identity.OwnerRecovery, error) {
	return identity.CompleteOwnerRecovery(ctx, s.db(), t, now)
}

func (s *PgStore) GrantAdmin(ctx context.Context, tenant, member uuid.UUID) error {
	return s.deps.WithTenantTx(ctx, tenant, func(db rolestore.DBTX) error {
		return rolestore.AssignRole(ctx, db, tenant, member, permission.RoleAdmin, nil)
	})
}

func (s *PgStore) registry() *tenancy.PostgresTenantRegistry {
	return tenancy.NewPostgresTenantRegistry(s.deps.Cfg.Shared)
}

func (s *PgStore) ScheduleDeletion(ctx context.Context, t uuid.UUID, now time.Time, retention time.Duration) error {
	return s.registry().RequestDeletion(ctx, t, now, retention)
}
func (s *PgStore) CancelDeletion(ctx context.Context, t uuid.UUID, now time.Time) error {
	return s.registry().CancelDeletion(ctx, t, now)
}
func (s *PgStore) CancelToken(ctx context.Context, t uuid.UUID, expiresAt time.Time) (string, error) {
	return identity.CreateDeletionCancelToken(ctx, s.db(), t, expiresAt)
}

func (s *PgStore) IdentityOf(ctx context.Context, t, m uuid.UUID) (uuid.UUID, error) {
	return identity.IdentityOf(ctx, s.deps.Cfg.Shared, t, m)
}
func (s *PgStore) VerifyPassword(ctx context.Context, id uuid.UUID, pw string, now time.Time) error {
	return identity.VerifyCurrentPassword(ctx, s.deps.Cfg.Shared, id, pw, now)
}
func (s *PgStore) MFAEnabled(ctx context.Context, id uuid.UUID) (bool, error) {
	return identity.MFAEnabled(ctx, s.deps.Cfg.Shared, id)
}

// VerifyMFA runs in its own transaction so that a wrong code is still counted (it is committed
// despite the failure) while any other failure rolls back.
func (s *PgStore) VerifyMFA(ctx context.Context, id uuid.UUID, code string, now time.Time) error {
	tx, err := s.deps.Cfg.Shared.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if err := identity.VerifyMFA(ctx, tx, id, code, now); err != nil {
		if identity.MFAAttemptCounts(err) {
			_ = tx.Commit(ctx) // keep the wrong-code count
		}
		return err
	}
	return tx.Commit(ctx)
}
