package apikeys

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/metap-org/metap/pkg/auth/identity"
)

// Store is where API keys live; only a hash of a key is ever kept. The service depends on this port,
// not on a database.
type Store interface {
	// Create issues a key for the member and returns the secret once. identity.ErrNotFound when the
	// member is not in the tenant.
	Create(ctx context.Context, tenant, member uuid.UUID, name string, expires *time.Time, createdBy *uuid.UUID, now time.Time) (string, *identity.APIKey, error)
	// List is the member's keys, newest first, revoked ones included.
	List(ctx context.Context, tenant, member uuid.UUID) ([]identity.APIKey, error)
	// KeyMember is the member a key belongs to; identity.ErrNotFound when the key is not the tenant's.
	KeyMember(ctx context.Context, tenant, key uuid.UUID) (uuid.UUID, error)
	// Revoke ends a key; ending one already ended is not an error. identity.ErrNotFound when the key
	// is not the tenant's.
	Revoke(ctx context.Context, tenant, key uuid.UUID, now time.Time) error
	// IsSupportSession says whether the member is a vendor's support session.
	IsSupportSession(ctx context.Context, tenant, member uuid.UUID) (bool, error)
}
