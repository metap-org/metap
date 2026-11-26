package credentials

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

// ErrNotFound: no credential with that id in the tenant. ErrNameTaken: another credential of the
// tenant already has the name (compared case-insensitively).
var (
	ErrNotFound  = errors.New("credential not found")
	ErrNameTaken = errors.New("credential name taken")
)

// Row is a credential as stored: everything but its value, which lives in the chosen home.
type Row struct {
	ID                   uuid.UUID
	Name, Description    string
	Kind, Source         string
	Username             string
	CreatedAt, UpdatedAt time.Time
}

// Store is the credential rows of a tenant on the shared database. Writes run on the transaction of
// the unit of work in ctx when there is one (kernel.IdentityDB), so a row and its audit entry are
// one transaction.
type Store interface {
	Get(ctx context.Context, tenant, id uuid.UUID) (Row, error)
	// List is the tenant's credentials by name, case-insensitively.
	List(ctx context.Context, tenant uuid.UUID) ([]Row, error)
	Insert(ctx context.Context, tenant uuid.UUID, r Row, createdBy *uuid.UUID) error
	// Update writes name, description, source and username; the kind never changes.
	Update(ctx context.Context, tenant uuid.UUID, r Row) error
	Delete(ctx context.Context, tenant, id uuid.UUID) error
	// Usage maps each credential id to what of the tenant's uses it.
	Usage(ctx context.Context, tenant uuid.UUID) (map[uuid.UUID][]*CredentialUse, error)
}
