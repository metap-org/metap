package users

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/metap-org/metap/internal/admin/kernel"
	"github.com/metap-org/metap/pkg/auth/identity"
	"github.com/metap-org/metap/pkg/permission"
	"github.com/metap-org/metap/pkg/permission/rolestore"
)

// PgStore keeps people on the shared database and their roles in the tenant's own.
type PgStore struct{ deps kernel.Deps }

func NewPgStore(deps kernel.Deps) *PgStore { return &PgStore{deps: deps} }

func (s *PgStore) db() kernel.IdentityDB { return s.deps.DB() }

func (s *PgStore) Members(ctx context.Context, t uuid.UUID, q identity.ListQuery) ([]identity.Member, int, error) {
	return identity.ListMembers(ctx, s.db(), t, q)
}
func (s *PgStore) Member(ctx context.Context, t, id uuid.UUID) (*identity.Member, error) {
	return identity.GetMember(ctx, s.db(), t, id)
}
func (s *PgStore) Add(ctx context.Context, in identity.NewMember) (*identity.Member, bool, error) {
	return identity.AddMember(ctx, s.db(), in)
}
func (s *PgStore) Rename(ctx context.Context, t, id uuid.UUID, name string) error {
	return identity.UpdateMember(ctx, s.db(), t, id, name)
}
func (s *PgStore) SetStatus(ctx context.Context, t, id uuid.UUID, status string, now time.Time) error {
	return identity.SetMemberStatus(ctx, s.db(), t, id, status, now)
}
func (s *PgStore) Remove(ctx context.Context, t, id uuid.UUID, now time.Time) error {
	return identity.RemoveMember(ctx, s.db(), t, id, now)
}
func (s *PgStore) Shared(ctx context.Context, t uuid.UUID, people []uuid.UUID) (map[uuid.UUID]bool, error) {
	return identity.SharedIdentities(ctx, s.db(), t, people)
}
func (s *PgStore) PendingInvitation(ctx context.Context, member uuid.UUID, now time.Time) (*identity.Invitation, error) {
	return identity.PendingInvitation(ctx, s.db(), member, now)
}

func (s *PgStore) NewInvitation(ctx context.Context, t uuid.UUID, m *identity.Member, by *uuid.UUID, now time.Time) (string, *identity.Invitation, bool, error) {
	newAccount, err := identity.CanSetPasswordOnAccept(ctx, s.db(), m.IdentityID, m.ID)
	if err != nil {
		return "", nil, false, err
	}
	token, inv, err := identity.CreateInvitation(ctx, s.db(), t, m.ID, m.IdentityID, newAccount, by, now)
	return token, inv, newAccount, err
}

func (s *PgStore) CheckPassword(ctx context.Context, person uuid.UUID, password string, tenant uuid.UUID) error {
	if tenant == uuid.Nil {
		return identity.CheckPasswordPolicy(ctx, s.db(), person, password, s.deps.Cfg.PasswordPolicy)
	}
	return identity.CheckPasswordPolicy(ctx, s.db(), person, password, s.deps.Cfg.PasswordPolicy, tenant)
}
func (s *PgStore) SetPassword(ctx context.Context, person uuid.UUID, pw string, now time.Time) error {
	return identity.SetPassword(ctx, s.db(), person, pw, now)
}
func (s *PgStore) DisableMFA(ctx context.Context, person uuid.UUID) error {
	return identity.DisableMFA(ctx, s.db(), person)
}
func (s *PgStore) CreateServiceAccount(ctx context.Context, t uuid.UUID, name string, by *uuid.UUID) (*identity.Member, error) {
	return identity.CreateServiceAccount(ctx, s.db(), t, name, by)
}
func (s *PgStore) ServiceAccounts(ctx context.Context, t uuid.UUID) ([]identity.Member, error) {
	return identity.ListServiceAccounts(ctx, s.db(), t)
}

func (s *PgStore) KnownRoles(ctx context.Context, t uuid.UUID) ([]string, error) {
	return s.deps.KnownRoles(ctx, t)
}

func (s *PgStore) RolesOf(ctx context.Context, t uuid.UUID, members []uuid.UUID) (roles map[uuid.UUID][]string, scoped map[uuid.UUID][]permission.ScopedRole, err error) {
	err = s.deps.WithTenantTx(ctx, t, func(db rolestore.DBTX) error {
		var err error
		if roles, err = rolestore.RolesForUsers(ctx, db, t, members); err != nil {
			return err
		}
		scoped, err = rolestore.ScopedRolesForUsers(ctx, db, t, members)
		return err
	})
	return
}

func (s *PgStore) WithRole(ctx context.Context, t uuid.UUID, role string) (ids []uuid.UUID, err error) {
	err = s.deps.WithTenantTx(ctx, t, func(db rolestore.DBTX) error {
		var err error
		ids, err = rolestore.UsersWithRole(ctx, db, t, role)
		return err
	})
	return
}

func (s *PgStore) Grant(ctx context.Context, t, member uuid.UUID, roles []string, by *uuid.UUID) error {
	return s.deps.WithTenantTx(ctx, t, func(db rolestore.DBTX) error {
		for _, r := range roles {
			if err := rolestore.AssignRole(ctx, db, t, member, r, by); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *PgStore) ChangeRole(ctx context.Context, t, member uuid.UUID, role string, assign bool, by *uuid.UUID) (roles []string, err error) {
	err = s.deps.WithTenantTx(ctx, t, func(db rolestore.DBTX) error {
		var err error
		if assign {
			err = rolestore.AssignRole(ctx, db, t, member, role, by)
		} else {
			err = rolestore.RevokeRole(ctx, db, t, member, role)
		}
		if err != nil {
			return err
		}
		held, err := rolestore.RolesForUsers(ctx, db, t, []uuid.UUID{member})
		roles = held[member]
		return err
	})
	return
}

func (s *PgStore) ChangeScopedRole(ctx context.Context, t, member uuid.UUID, role, scopeType, scopeID string, assign bool, by *uuid.UUID) (now []permission.ScopedRole, err error) {
	err = s.deps.WithTenantTx(ctx, t, func(db rolestore.DBTX) error {
		var err error
		if assign {
			err = rolestore.AssignScopedRole(ctx, db, t, member, role, scopeType, scopeID, by)
		} else {
			err = rolestore.RevokeScopedRole(ctx, db, t, member, role, scopeType, scopeID)
		}
		if err != nil {
			return err
		}
		held, err := rolestore.ScopedRolesForUsers(ctx, db, t, []uuid.UUID{member})
		now = held[member]
		return err
	})
	return
}

func (s *PgStore) RevokeAll(ctx context.Context, t, member uuid.UUID) error {
	return s.deps.WithTenantTx(ctx, t, func(db rolestore.DBTX) error {
		return rolestore.RevokeAllRoles(ctx, db, t, member)
	})
}

func (s *PgStore) ScopeMembers(ctx context.Context, t uuid.UUID, scopeType, scopeID string) (out []rolestore.ScopeMember, err error) {
	err = s.deps.WithTenantTx(ctx, t, func(db rolestore.DBTX) error {
		var err error
		out, err = rolestore.MembersOfScope(ctx, db, t, scopeType, scopeID)
		return err
	})
	return
}
