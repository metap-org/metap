package apikeys

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/metap-org/metap/pkg/auth/identity"
)

// PgStore keeps API keys in the shared identity database.
type PgStore struct{ db identity.DBTX }

func NewPgStore(db identity.DBTX) *PgStore { return &PgStore{db: db} }

func (s *PgStore) Create(ctx context.Context, tenant, member uuid.UUID, name string, expires *time.Time, createdBy *uuid.UUID, now time.Time) (string, *identity.APIKey, error) {
	return identity.CreateAPIKey(ctx, s.db, tenant, member, name, expires, createdBy, now)
}

func (s *PgStore) List(ctx context.Context, tenant, member uuid.UUID) ([]identity.APIKey, error) {
	return identity.ListAPIKeys(ctx, s.db, tenant, member)
}

func (s *PgStore) KeyMember(ctx context.Context, tenant, key uuid.UUID) (uuid.UUID, error) {
	return identity.KeyTenant(ctx, s.db, tenant, key)
}

func (s *PgStore) Revoke(ctx context.Context, tenant, key uuid.UUID, now time.Time) error {
	return identity.RevokeAPIKey(ctx, s.db, tenant, key, now)
}

func (s *PgStore) IsSupportSession(ctx context.Context, tenant, member uuid.UUID) (bool, error) {
	return identity.IsSupportSessionMember(ctx, s.db, tenant, member)
}
