package authctx

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/metap-org/metap/pkg/auth/identity"
	"github.com/metap-org/metap/pkg/permission"
	"github.com/metap-org/metap/pkg/permission/rolestore"
	"github.com/metap-org/metap/pkg/tenancy"
)

// Store is what resolving a caller reads: whether the session behind a token still stands, the API key a
// token names, the roles a member holds and the attributes of their membership record. Errors are
// identity's (ErrDisabled, ErrSessionRevoked, ...), which the HTTP layer maps onto a 401.
type Store interface {
	// CheckSession fails when the member was disabled or removed, or every session was revoked after
	// the token was issued (issuedAt nil: only the member is checked).
	CheckSession(ctx context.Context, tenant, member uuid.UUID, issuedAt *time.Time) error
	// CheckSessionID fails when the login session is revoked, expired or unknown.
	CheckSessionID(ctx context.Context, tenant, member, session uuid.UUID, now time.Time) error
	// ResolveAPIKey is the key a presented "mk_..." token names, if it is live.
	ResolveAPIKey(ctx context.Context, token string, now time.Time) (*identity.APIKey, error)
	// Roles is the member's tenant-wide roles and the roles they hold only inside a scope, from one
	// transaction on the tenant's own database.
	Roles(ctx context.Context, tenant, member uuid.UUID) (Held, error)
	// ContextAttributes is the member's row in the entity's table (nil: none).
	ContextAttributes(ctx context.Context, tenant uuid.UUID, table string, member uuid.UUID) (map[string]any, error)
}

// Held is what the role lookup returns for one member.
type Held struct {
	Roles  []string
	Scoped []permission.ScopedRole
}

// PgStore reads the shared identity database and, through the router, the tenant's own.
type PgStore struct{ router *tenancy.Router }

func NewPgStore(router *tenancy.Router) *PgStore { return &PgStore{router: router} }

func (s *PgStore) CheckSession(ctx context.Context, tenant, member uuid.UUID, issuedAt *time.Time) error {
	return identity.CheckSession(ctx, s.router.SharedPool(), tenant, member, issuedAt)
}

func (s *PgStore) CheckSessionID(ctx context.Context, tenant, member, session uuid.UUID, now time.Time) error {
	return identity.CheckSessionID(ctx, s.router.SharedPool(), tenant, member, session, now)
}

func (s *PgStore) ResolveAPIKey(ctx context.Context, token string, now time.Time) (*identity.APIKey, error) {
	return identity.ResolveAPIKey(ctx, s.router.SharedPool(), token, now)
}

// Roles is routed through the router, not a bare pool: a DedicatedDb tenant's user_roles table lives only
// in that tenant's own database. The scoped roles (roadmap 115 M1) come from the same transaction, so they
// are exactly as fresh as the roles.
func (s *PgStore) Roles(ctx context.Context, tenant, member uuid.UUID) (Held, error) {
	tx, err := s.router.Begin(ctx, tenant)
	if err != nil {
		return Held{}, err
	}
	roles, err := rolestore.GetRolesForUser(ctx, tx, tenant, member)
	if err != nil {
		tx.Rollback(ctx) //nolint:errcheck
		return Held{}, err
	}
	scoped, err := rolestore.ScopedRolesForUser(ctx, tx, tenant, member)
	if err != nil {
		tx.Rollback(ctx) //nolint:errcheck
		return Held{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Held{}, err
	}
	return Held{Roles: roles, Scoped: scoped}, nil
}

func (s *PgStore) ContextAttributes(ctx context.Context, tenant uuid.UUID, table string, member uuid.UUID) (map[string]any, error) {
	tx, err := s.router.Begin(ctx, tenant)
	if err != nil {
		return nil, err
	}
	result, err := identity.FetchContextAttributes(ctx, tx, tenant, table, member)
	if err != nil {
		tx.Rollback(ctx) //nolint:errcheck
		return nil, err
	}
	return result, tx.Commit(ctx)
}
