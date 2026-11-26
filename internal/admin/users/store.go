package users

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/metap-org/metap/internal/admin/kernel"
	"github.com/metap-org/metap/pkg/auth/identity"
	"github.com/metap-org/metap/pkg/auth/mail"
	"github.com/metap-org/metap/pkg/permission"
	"github.com/metap-org/metap/pkg/permission/rolestore"
)

// Store is a tenant's people: the members and their passwords on the shared identity database, and the
// roles they hold in the tenant's own. Writes run on the transaction of the unit of work in ctx when
// there is one (kernel.IdentityDB, Router), so a change and its audit entry are one transaction. Errors
// are identity's and rolestore's, which the service maps onto the envelope.
type Store interface {
	// --- members (shared database)
	Members(ctx context.Context, tenant uuid.UUID, q identity.ListQuery) ([]identity.Member, int, error)
	Member(ctx context.Context, tenant, id uuid.UUID) (*identity.Member, error)
	// Add makes the member (and the person, when the address is new); created says whether it did.
	Add(ctx context.Context, in identity.NewMember) (m *identity.Member, created bool, err error)
	Rename(ctx context.Context, tenant, id uuid.UUID, displayName string) error
	SetStatus(ctx context.Context, tenant, id uuid.UUID, status string, now time.Time) error
	Remove(ctx context.Context, tenant, id uuid.UUID, now time.Time) error
	// Shared says, per person, whether they are a member of more than this tenant.
	Shared(ctx context.Context, tenant uuid.UUID, people []uuid.UUID) (map[uuid.UUID]bool, error)
	// PendingInvitation is the member's invitation that has not been used or expired (nil: none).
	PendingInvitation(ctx context.Context, member uuid.UUID, now time.Time) (*identity.Invitation, error)
	// NewInvitation makes a fresh invitation for an invited member and says whether the person has to
	// choose a password when accepting.
	NewInvitation(ctx context.Context, tenant uuid.UUID, m *identity.Member, by *uuid.UUID, now time.Time) (token string, inv *identity.Invitation, newAccount bool, err error)
	// CheckPassword is nil when password passes the deployment's policy; tenant (may be uuid.Nil) adds
	// that tenant's own policy.
	CheckPassword(ctx context.Context, person uuid.UUID, password string, tenant uuid.UUID) error
	SetPassword(ctx context.Context, person uuid.UUID, password string, now time.Time) error
	DisableMFA(ctx context.Context, person uuid.UUID) error
	CreateServiceAccount(ctx context.Context, tenant uuid.UUID, name string, by *uuid.UUID) (*identity.Member, error)
	ServiceAccounts(ctx context.Context, tenant uuid.UUID) ([]identity.Member, error)

	// --- roles (the tenant's own database)
	KnownRoles(ctx context.Context, tenant uuid.UUID) ([]string, error)
	RolesOf(ctx context.Context, tenant uuid.UUID, members []uuid.UUID) (map[uuid.UUID][]string, map[uuid.UUID][]permission.ScopedRole, error)
	// WithRole is who holds the role tenant-wide.
	WithRole(ctx context.Context, tenant uuid.UUID, role string) ([]uuid.UUID, error)
	// Grant gives the member each role tenant-wide.
	Grant(ctx context.Context, tenant, member uuid.UUID, roles []string, by *uuid.UUID) error
	// ChangeRole gives (assign) or takes away a tenant-wide role and returns what the member then holds.
	ChangeRole(ctx context.Context, tenant, member uuid.UUID, role string, assign bool, by *uuid.UUID) ([]string, error)
	// ChangeScopedRole is ChangeRole for a role held only on one scope; it returns what the member then
	// holds on scopes.
	ChangeScopedRole(ctx context.Context, tenant, member uuid.UUID, role, scopeType, scopeID string, assign bool, by *uuid.UUID) ([]permission.ScopedRole, error)
	RevokeAll(ctx context.Context, tenant, member uuid.UUID) error
	ScopeMembers(ctx context.Context, tenant uuid.UUID, scopeType, scopeID string) ([]rolestore.ScopeMember, error)
}

// Owners is the tenant's owner, who other admins may not touch (roadmap 115 M8).
type Owners interface {
	// Ensure is the tenant's owner (uuid.Nil: none yet), assigning one the first time anything asks.
	Ensure(ctx context.Context, tenant uuid.UUID) (uuid.UUID, error)
	// Guard refuses an action on the owner by anyone but the owner.
	Guard(ctx context.Context, c kernel.Caller, target uuid.UUID, what string) error
}

// Notifier emails an invitation (mail.Mailer does; nil sends nothing and the answer carries the link).
type Notifier interface {
	Link(path, token string) string
	SendInvitation(ctx context.Context, inv mail.Invitation) error
}
