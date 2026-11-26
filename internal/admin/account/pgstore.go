package account

import (
	"context"

	"github.com/google/uuid"

	"github.com/metap-org/metap/internal/admin/kernel"
	"github.com/metap-org/metap/pkg/auth/identity"
)

// PgStore reads and writes the shared identity database.
type PgStore struct{ deps kernel.Deps }

func NewPgStore(deps kernel.Deps) *PgStore { return &PgStore{deps: deps} }

func (s *PgStore) db() kernel.IdentityDB { return s.deps.DB() }

func (s *PgStore) Member(ctx context.Context, t, id uuid.UUID) (*identity.Member, error) {
	return identity.GetMember(ctx, s.db(), t, id)
}
func (s *PgStore) Rename(ctx context.Context, t, id uuid.UUID, name string) error {
	return identity.UpdateMember(ctx, s.db(), t, id, name)
}
func (s *PgStore) IdentityOf(ctx context.Context, t, member uuid.UUID) (uuid.UUID, error) {
	return identity.IdentityOf(ctx, s.db(), t, member)
}
func (s *PgStore) Memberships(ctx context.Context, person uuid.UUID) ([]identity.Membership, error) {
	return identity.Memberships(ctx, s.db(), person)
}
func (s *PgStore) Members(ctx context.Context, t uuid.UUID, q identity.ListQuery) ([]identity.Member, int, error) {
	return identity.ListMembers(ctx, s.db(), t, q)
}
