package support

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/metap-org/metap/pkg/auth/identity"
)

// Store is where support grants live, and everything the slice needs from the rest of the system to
// start a session: the vendor person, a service account in the tenant holding the granted role, an
// API key, and the tenant's registry. Every error is pkg/auth/identity's or the tenant registry's,
// which kernel.TenantErr maps onto the platform envelope.
//
// Grants, accounts, keys and sessions live in the shared database; the role lives in the tenant's own
// (AssignRole, ClearRoles, KnownRoles), which a change on the shared database must not open itself
// (a second connection while the change holds the first), so the service does that work before or
// after the change.
type Store interface {
	CreateGrant(ctx context.Context, tenant, by uuid.UUID, reason, role string, hours int, now time.Time) (*identity.SupportGrant, error)
	ListGrants(ctx context.Context, tenant uuid.UUID) ([]identity.SupportGrant, error)
	GetGrant(ctx context.Context, tenant, id uuid.UUID) (*identity.SupportGrant, error)
	LatestActiveGrant(ctx context.Context, tenant uuid.UUID, now time.Time) (*identity.SupportGrant, error)
	// RevokeGrant ends a grant and removes its session members, returning them.
	RevokeGrant(ctx context.Context, tenant, id uuid.UUID, now time.Time) ([]uuid.UUID, error)
	// ExpireSessions removes the members of lapsed sessions and returns them.
	ExpireSessions(ctx context.Context, tenant uuid.UUID, now time.Time) ([]uuid.UUID, error)

	// TenantReachable is nil when the tenant can be served (not suspended or gone).
	TenantReachable(ctx context.Context, tenant uuid.UUID) error
	// Person is a member of a tenant (the vendor's support person, of the platform tenant).
	Person(ctx context.Context, tenant, id uuid.UUID) (*identity.Member, error)
	CreateAccount(ctx context.Context, tenant uuid.UUID, name string) (*identity.Member, error)
	RemoveMember(ctx context.Context, tenant, id uuid.UUID, now time.Time) error
	CreateKey(ctx context.Context, tenant, member uuid.UUID, name string, expires time.Time, now time.Time) (string, *identity.APIKey, error)
	RecordSession(ctx context.Context, tenant uuid.UUID, s identity.SupportSession) error

	// In the tenant's own database:
	KnownRoles(ctx context.Context, tenant uuid.UUID) ([]string, error)
	AssignRole(ctx context.Context, tenant, member uuid.UUID, role string) error
	ClearRoles(ctx context.Context, tenant uuid.UUID, members []uuid.UUID) error
}
