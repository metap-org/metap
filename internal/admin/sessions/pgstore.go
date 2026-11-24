package sessions

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/metap-org/metap/pkg/auth/identity"
)

// PgStore keeps sessions in the shared identity database.
type PgStore struct{ db identity.DBTX }

func NewPgStore(db identity.DBTX) *PgStore { return &PgStore{db: db} }

func (s *PgStore) IdentityOf(ctx context.Context, tenant, member uuid.UUID) (uuid.UUID, error) {
	return identity.IdentityOf(ctx, s.db, tenant, member)
}

func (s *PgStore) List(ctx context.Context, identityID uuid.UUID, now time.Time) ([]identity.Session, error) {
	return identity.ListSessions(ctx, s.db, identityID, now)
}

func (s *PgStore) Revoke(ctx context.Context, identityID, sessionID uuid.UUID, now time.Time) error {
	return identity.RevokeSession(ctx, s.db, identityID, sessionID, now)
}

func (s *PgStore) RevokeOthers(ctx context.Context, identityID, keep uuid.UUID, now time.Time) (int, error) {
	return identity.RevokeOtherSessions(ctx, s.db, identityID, keep, now)
}
