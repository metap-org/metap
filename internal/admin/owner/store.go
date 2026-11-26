package owner

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/metap-org/metap/pkg/auth/identity"
	"github.com/metap-org/metap/pkg/auth/mail"
)

// Store is who owns a tenant and what a recovery of that ownership needs: the identity database, the
// tenant's own role table (the new owner becomes an admin there) and the tenant registry (deleting the
// workspace). Writes run on the transaction of the unit of work in ctx when there is one
// (kernel.IdentityDB), so a change and its audit entry are one transaction. Errors are identity's and
// tenancy's, which the service maps onto the envelope.
type Store interface {
	OwnerID(ctx context.Context, tenant uuid.UUID) (uuid.UUID, error)
	SetOwner(ctx context.Context, tenant, member uuid.UUID) error
	// ActiveAdmins is the tenant's active people holding the tenant-wide admin role (service accounts
	// are not people).
	ActiveAdmins(ctx context.Context, tenant uuid.UUID) ([]identity.Member, error)
	// ActivePeople is every active person of the tenant, whatever their roles.
	ActivePeople(ctx context.Context, tenant uuid.UUID) ([]identity.Member, error)
	Member(ctx context.Context, tenant, id uuid.UUID) (*identity.Member, error)
	TenantName(ctx context.Context, tenant uuid.UUID) string

	PendingRecovery(ctx context.Context, tenant uuid.UUID) (*identity.OwnerRecovery, error)
	RequestRecovery(ctx context.Context, tenant, newOwner, by uuid.UUID, byEmail, reason string, now time.Time) (*identity.OwnerRecovery, error)
	CancelRecovery(ctx context.Context, tenant, by uuid.UUID, now time.Time) (*identity.OwnerRecovery, error)
	CompleteRecovery(ctx context.Context, tenant uuid.UUID, now time.Time) (*identity.OwnerRecovery, error)
	// GrantAdmin gives the member the tenant-wide admin role.
	GrantAdmin(ctx context.Context, tenant, member uuid.UUID) error

	// ScheduleDeletion suspends the workspace for retention and then deletes it; CancelDeletion undoes it.
	ScheduleDeletion(ctx context.Context, tenant uuid.UUID, now time.Time, retention time.Duration) error
	CancelDeletion(ctx context.Context, tenant uuid.UUID, now time.Time) error
	// CancelToken is the secret a cancellation link carries, valid until expiresAt.
	CancelToken(ctx context.Context, tenant uuid.UUID, expiresAt time.Time) (string, error)

	// IdentityOf is the person behind a member (one person may be a member of several tenants).
	IdentityOf(ctx context.Context, tenant, member uuid.UUID) (uuid.UUID, error)
	// VerifyPassword checks the current password; a wrong one counts toward the sign-in lockout.
	VerifyPassword(ctx context.Context, id uuid.UUID, password string, now time.Time) error
	MFAEnabled(ctx context.Context, id uuid.UUID) (bool, error)
	// VerifyMFA checks a second-factor code; a wrong code is counted even though the call fails.
	VerifyMFA(ctx context.Context, id uuid.UUID, code string, now time.Time) error
}

// Notifier emails a tenant's admins (mail.Mailer does). Nil sends nothing.
type Notifier interface {
	Configured() bool
	Link(path, token string) string
	SendOwnerRecoveryNotice(ctx context.Context, n mail.OwnerRecoveryNotice) error
	SendWorkspaceDeletionNotice(ctx context.Context, n mail.WorkspaceDeletionNotice) error
}
