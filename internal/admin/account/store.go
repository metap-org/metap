package account

import (
	"context"

	"github.com/google/uuid"

	"github.com/metap-org/metap/pkg/auth/identity"
)

// Store is what a signed-in person can see and change about themselves, and who shares their tenant.
type Store interface {
	Member(ctx context.Context, tenant, id uuid.UUID) (*identity.Member, error)
	Rename(ctx context.Context, tenant, id uuid.UUID, displayName string) error
	// IdentityOf is the person behind a member (identity.ErrNotFound: none).
	IdentityOf(ctx context.Context, tenant, member uuid.UUID) (uuid.UUID, error)
	// Memberships is every tenant the person belongs to.
	Memberships(ctx context.Context, person uuid.UUID) ([]identity.Membership, error)
	Members(ctx context.Context, tenant uuid.UUID, q identity.ListQuery) ([]identity.Member, int, error)
}
