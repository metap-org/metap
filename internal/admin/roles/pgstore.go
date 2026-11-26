package roles

import (
	"context"

	"github.com/google/uuid"

	"github.com/metap-org/metap/internal/admin/kernel"
	"github.com/metap-org/metap/pkg/auth/identity"
	"github.com/metap-org/metap/pkg/permission/rolestore"
)

// PgStore keeps roles in the tenant's own database (through the router) and counts holders on the
// shared identity database.
type PgStore struct{ deps kernel.Deps }

func NewPgStore(deps kernel.Deps) *PgStore { return &PgStore{deps: deps} }

func (s *PgStore) tx(ctx context.Context, tenant uuid.UUID, fn func(db rolestore.DBTX) error) error {
	return s.deps.WithTenantTx(ctx, tenant, func(db rolestore.DBTX) error {
		if err := rolestore.EnsureBuiltinRoles(ctx, db, tenant); err != nil {
			return err
		}
		return fn(db)
	})
}

func (s *PgStore) Catalog(ctx context.Context, tenant uuid.UUID, search string) (rows []rolestore.Role, counts map[string]int, err error) {
	err = s.tx(ctx, tenant, func(db rolestore.DBTX) error {
		var err error
		if rows, err = rolestore.ListRoleRows(ctx, db, tenant, search); err != nil {
			return err
		}
		counts, err = rolestore.PolicyCountsByRole(ctx, db, tenant)
		return err
	})
	return
}

func (s *PgStore) Get(ctx context.Context, tenant uuid.UUID, name string) (r rolestore.Role, policies []rolestore.PolicyRef, err error) {
	err = s.tx(ctx, tenant, func(db rolestore.DBTX) error {
		var err error
		if r, err = rolestore.GetRoleByName(ctx, db, tenant, name); err != nil {
			return err
		}
		policies, err = rolestore.PoliciesNamingRole(ctx, db, tenant, name)
		return err
	})
	return
}

func (s *PgStore) Role(ctx context.Context, tenant uuid.UUID, name string) (r rolestore.Role, err error) {
	err = s.deps.WithTenantTx(ctx, tenant, func(db rolestore.DBTX) error {
		var err error
		r, err = rolestore.GetRoleByName(ctx, db, tenant, name)
		return err
	})
	return
}

func (s *PgStore) ActiveHolders(ctx context.Context, tenant uuid.UUID, names []string) (map[string]int, error) {
	holders := map[string][]uuid.UUID{}
	if err := s.deps.WithTenantTx(ctx, tenant, func(db rolestore.DBTX) error {
		for _, r := range names {
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
	out := make(map[string]int, len(names))
	for _, r := range names {
		if len(holders[r]) == 0 {
			continue
		}
		_, n, err := identity.ListMembers(ctx, s.deps.Cfg.Shared, tenant, identity.ListQuery{
			IDs: holders[r], Status: identity.StatusActive, Limit: 1})
		if err != nil {
			return nil, err
		}
		out[r] = n
	}
	return out, nil
}

func (s *PgStore) HolderEmails(ctx context.Context, tenant uuid.UUID, ids []uuid.UUID, limit int) ([]string, error) {
	ms, _, err := identity.ListMembers(ctx, s.deps.Cfg.Shared, tenant, identity.ListQuery{IDs: ids, Limit: limit})
	if err != nil {
		return nil, err
	}
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.Email
	}
	return out, nil
}

func (s *PgStore) Create(ctx context.Context, tenant uuid.UUID, name, label, description, copyFrom string, by *uuid.UUID) (created rolestore.Role, copiedN int, err error) {
	err = s.deps.WithTenantTx(ctx, tenant, func(db rolestore.DBTX) error {
		var copied []rolestore.RoleGrant
		if copyFrom != "" {
			if _, err := rolestore.GetRoleByName(ctx, db, tenant, copyFrom); err != nil {
				return err
			}
			var err error
			if copied, err = rolestore.RoleBasicGrants(ctx, db, tenant, copyFrom); err != nil {
				return err
			}
		}
		var err error
		if created, err = rolestore.CreateRole(ctx, db, tenant, name, label, description, by); err != nil {
			return err
		}
		copiedN = len(copied)
		if copiedN > 0 {
			_, err = rolestore.ReplaceRoleBasicGrants(ctx, db, tenant, name, copied, by)
		}
		return err
	})
	return
}

func (s *PgStore) Update(ctx context.Context, tenant uuid.UUID, name, label, description string) (before, after rolestore.Role, err error) {
	err = s.tx(ctx, tenant, func(db rolestore.DBTX) error {
		var err error
		if before, err = rolestore.GetRoleByName(ctx, db, tenant, name); err != nil {
			return err
		}
		after, err = rolestore.UpdateRole(ctx, db, tenant, name, label, description)
		return err
	})
	return
}

func (s *PgStore) Usage(ctx context.Context, tenant uuid.UUID, name string) (r rolestore.Role, holders []uuid.UUID, policies []rolestore.PolicyRef, err error) {
	err = s.tx(ctx, tenant, func(db rolestore.DBTX) error {
		var err error
		if r, err = rolestore.GetRoleByName(ctx, db, tenant, name); err != nil {
			return err
		}
		if holders, err = rolestore.UsersWithRole(ctx, db, tenant, name); err != nil {
			return err
		}
		policies, err = rolestore.PoliciesNamingRole(ctx, db, tenant, name)
		return err
	})
	return
}

func (s *PgStore) Delete(ctx context.Context, tenant uuid.UUID, name string) error {
	return s.deps.WithTenantTx(ctx, tenant, func(db rolestore.DBTX) error {
		return rolestore.DeleteRole(ctx, db, tenant, name)
	})
}

func (s *PgStore) Permissions(ctx context.Context, tenant uuid.UUID, name string) (grants []rolestore.RoleGrant, advanced []rolestore.AdvancedPolicy, err error) {
	err = s.tx(ctx, tenant, func(db rolestore.DBTX) error {
		if _, err := rolestore.GetRoleByName(ctx, db, tenant, name); err != nil {
			return err
		}
		var err error
		if grants, err = rolestore.RoleBasicGrants(ctx, db, tenant, name); err != nil {
			return err
		}
		advanced, err = rolestore.AdvancedPoliciesNamingRole(ctx, db, tenant, name)
		return err
	})
	return
}

func (s *PgStore) ReplaceGrants(ctx context.Context, tenant uuid.UUID, name string, grants []rolestore.RoleGrant, by *uuid.UUID) (role rolestore.Role, before []rolestore.RoleGrant, err error) {
	err = s.deps.WithTenantTx(ctx, tenant, func(db rolestore.DBTX) error {
		var err error
		if role, err = rolestore.GetRoleByName(ctx, db, tenant, name); err != nil {
			return err
		}
		if before, err = rolestore.RoleBasicGrants(ctx, db, tenant, name); err != nil {
			return err
		}
		_, err = rolestore.ReplaceRoleBasicGrants(ctx, db, tenant, name, grants, by)
		return err
	})
	return
}
