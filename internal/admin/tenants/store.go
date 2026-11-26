package tenants

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/metap-org/metap/pkg/auth/identity"
	"github.com/metap-org/metap/pkg/auth/mail"
	"github.com/metap-org/metap/pkg/tenancy"
)

// Store is the tenant registry and what creating a tenant touches on the shared database: its first
// admin, the built-in roles, the invitation. Every method runs on the transaction of the unit of
// work in its context when there is one (kernel.IdentityDB), so a creation and its audit entry are
// one transaction. Errors are tenancy's and identity's, which kernel.TenantErr maps onto the envelope.
type Store interface {
	Detail(ctx context.Context, id uuid.UUID) (*tenancy.TenantDetail, error)
	Search(ctx context.Context, f tenancy.TenantFilter) ([]tenancy.TenantDetail, int, error)
	// MemberCount is how many live members the tenant has.
	MemberCount(ctx context.Context, id uuid.UUID) (int32, error)

	// Register adds the tenant row in the "provisioning" state; Deprovision marks it deleted again
	// when what follows fails.
	Register(ctx context.Context, id uuid.UUID, name, tier string) error
	Deprovision(ctx context.Context, id uuid.UUID) error
	SetProduct(ctx context.Context, id uuid.UUID, product string) error

	// CreateAdmin makes the first admin (invited), the built-in roles, the admin role assignment and
	// the invitation, and activates the tenant: one change, all or none.
	CreateAdmin(ctx context.Context, tenant uuid.UUID, in NewAdmin, invitedBy *uuid.UUID, now time.Time) (*Admin, error)

	Update(ctx context.Context, id uuid.UUID, name, tier *string) error
	Suspend(ctx context.Context, id uuid.UUID) error
	Resume(ctx context.Context, id uuid.UUID) error
	RequestDeletion(ctx context.Context, id uuid.UUID, now time.Time, retention time.Duration) error
	CancelDeletion(ctx context.Context, id uuid.UUID, now time.Time) error
}

// NewAdmin is who a new tenant's first admin is.
type NewAdmin struct {
	Email, DisplayName string
}

// Admin is what creating the first admin produced.
type Admin struct {
	Member     *identity.Member
	Token      string
	Invitation *identity.Invitation
	NewAccount bool
}

// Mailer sends the invitation (mail.Mailer does; nil sends nothing and the answer carries the link).
type Mailer interface {
	Link(path, token string) string
	SendInvitation(ctx context.Context, inv mail.Invitation) error
}
