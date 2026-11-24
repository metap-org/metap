package sessions

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/metap-org/metap/pkg/auth/identity"
)

// Store is where login sessions live. A person's sessions belong to their identity (one person, many
// tenants), not to one membership. The service depends on this port, not on a database.
type Store interface {
	// IdentityOf is the person behind a membership; identity.ErrNotFound when there is none.
	IdentityOf(ctx context.Context, tenant, member uuid.UUID) (uuid.UUID, error)
	// List is the identity's live sessions (not revoked, not past expiry), newest activity first.
	List(ctx context.Context, identityID uuid.UUID, now time.Time) ([]identity.Session, error)
	// Revoke ends one of the identity's own live sessions; identity.ErrNotFound when it is not
	// theirs or is already over (a person cannot probe another person's session ids).
	Revoke(ctx context.Context, identityID, sessionID uuid.UUID, now time.Time) error
	// RevokeOthers ends every live session of the identity except keep and says how many.
	RevokeOthers(ctx context.Context, identityID, keep uuid.UUID, now time.Time) (int, error)
}
