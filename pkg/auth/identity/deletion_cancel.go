package identity

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ErrCancelInvalid: unknown, expired or already used cancel link. One error for all three, so the
// page cannot be used to probe which.
var ErrCancelInvalid = errors.New("this link is invalid or has expired")

// CreateDeletionCancelToken issues the link token that cancels a tenant's deletion until expiresAt.
// The token is returned once and never stored, only its hash.
func CreateDeletionCancelToken(ctx context.Context, db DBTX, tenant uuid.UUID, expiresAt time.Time) (string, error) {
	token, hash, err := newToken()
	if err != nil {
		return "", err
	}
	if _, err := db.Exec(ctx, `INSERT INTO metadata.deletion_cancel_tokens (tenant_id, token_hash, expires_at) VALUES ($1, $2, $3)`,
		tenant, hash, expiresAt); err != nil {
		return "", err
	}
	return token, nil
}

// ConsumeDeletionCancelToken uses a cancel token and returns the tenant it belongs to. A token works
// once.
func ConsumeDeletionCancelToken(ctx context.Context, db DBTX, token string, now time.Time) (uuid.UUID, error) {
	var tenant uuid.UUID
	err := db.QueryRow(ctx, `UPDATE metadata.deletion_cancel_tokens SET used_at = $2
		WHERE token_hash = $1 AND used_at IS NULL AND expires_at > $2 RETURNING tenant_id`,
		hashToken(token), now).Scan(&tenant)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrCancelInvalid
	}
	return tenant, err
}
