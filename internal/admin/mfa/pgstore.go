package mfa

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/metap-org/metap/pkg/auth/identity"
)

// DB is what PgStore needs: queries, and transactions for the two checks that act on a code.
type DB interface {
	identity.DBTX
	Begin(ctx context.Context) (pgx.Tx, error)
}

// PgStore keeps the factor in the shared identity database.
type PgStore struct{ db DB }

func NewPgStore(db DB) *PgStore { return &PgStore{db: db} }

func (s *PgStore) IdentityOf(ctx context.Context, tenant, member uuid.UUID) (uuid.UUID, error) {
	return identity.IdentityOf(ctx, s.db, tenant, member)
}

func (s *PgStore) Email(ctx context.Context, id uuid.UUID) (string, error) {
	ident, err := identity.FindIdentityByID(ctx, s.db, id)
	if err != nil {
		return "", err
	}
	return ident.Email, nil
}

func (s *PgStore) Status(ctx context.Context, id uuid.UUID) (bool, int, error) {
	on, err := identity.MFAEnabled(ctx, s.db, id)
	if err != nil || !on {
		return false, 0, err
	}
	left, err := identity.RecoveryCodesLeft(ctx, s.db, id)
	return true, left, err
}

func (s *PgStore) Begin(ctx context.Context, id uuid.UUID, account string, now time.Time) (string, string, error) {
	return identity.BeginMFAEnrollment(ctx, s.db, id, account, now)
}

func (s *PgStore) Confirm(ctx context.Context, id uuid.UUID, code string, now time.Time) ([]string, error) {
	var codes []string
	err := s.inTx(ctx, func(db identity.DBTX) error {
		var err error
		codes, err = identity.ConfirmMFAEnrollment(ctx, db, id, code, now)
		return err
	})
	return codes, err
}

func (s *PgStore) Disable(ctx context.Context, id uuid.UUID, code string, now time.Time) error {
	return s.inTx(ctx, func(db identity.DBTX) error {
		if err := identity.VerifyMFA(ctx, db, id, code, now); err != nil {
			return err
		}
		return identity.DisableMFA(ctx, db, id)
	})
}

func (s *PgStore) Regenerate(ctx context.Context, id uuid.UUID, code string, now time.Time) ([]string, error) {
	var codes []string
	err := s.inTx(ctx, func(db identity.DBTX) error {
		if err := identity.VerifyMFA(ctx, db, id, code, now); err != nil {
			return err
		}
		var err error
		codes, err = identity.RegenerateRecoveryCodes(ctx, db, id)
		return err
	})
	return codes, err
}

// inTx runs fn in one transaction on the shared database. A wrong code is committed with its count:
// rolling it back would undo the very counter that stops guessing.
func (s *PgStore) inTx(ctx context.Context, fn func(db identity.DBTX) error) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := fn(tx); err != nil {
		if identity.MFAAttemptCounts(err) {
			_ = tx.Commit(ctx)
		}
		return err
	}
	return tx.Commit(ctx)
}
