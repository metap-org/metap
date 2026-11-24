package preferences

import (
	"context"

	"github.com/google/uuid"

	"github.com/metap-org/metap/pkg/auth/identity"
)

// PgStore keeps preferences in the shared identity database.
type PgStore struct{ db identity.DBTX }

func NewPgStore(db identity.DBTX) *PgStore { return &PgStore{db: db} }

func (s *PgStore) Locale(ctx context.Context, tenant, user uuid.UUID) (string, error) {
	return identity.GetLocale(ctx, s.db, tenant, user)
}

func (s *PgStore) SetLocale(ctx context.Context, tenant, user uuid.UUID, locale string) error {
	return identity.SetLocale(ctx, s.db, tenant, user, locale)
}
