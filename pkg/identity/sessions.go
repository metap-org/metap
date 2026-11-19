package identity

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// SessionTouchEvery is how often a busy session's last_seen_at is written.
const SessionTouchEvery = time.Minute

// Session is one sign-in of a person.
type Session struct {
	ID         uuid.UUID
	IdentityID uuid.UUID
	MemberID   uuid.UUID
	TenantID   uuid.UUID
	TenantName string
	CreatedAt  time.Time
	LastSeenAt time.Time
	ExpiresAt  time.Time
	IP         string
	UserAgent  string
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n]
	}
	return s
}

// CreateSession records a sign-in and returns its id (the token's "sid").
func CreateSession(ctx context.Context, db DBTX, identityID, memberID, tenantID uuid.UUID, ttl time.Duration, ip, userAgent string, now time.Time) (uuid.UUID, error) {
	var id uuid.UUID
	err := db.QueryRow(ctx, `INSERT INTO metadata.sessions (identity_id, member_id, tenant_id, created_at, last_seen_at, expires_at, ip, user_agent)
		VALUES ($1,$2,$3,$4,$4,$5,$6,$7) RETURNING id`,
		identityID, memberID, tenantID, now, now.Add(ttl), nullIfEmpty(clip(ip, 64)), nullIfEmpty(clip(userAgent, 300))).Scan(&id)
	return id, err
}

func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// CheckSessionID fails with ErrSessionRevoked when the session behind a token was revoked or no
// longer exists, and otherwise records activity (at most once per SessionTouchEvery).
func CheckSessionID(ctx context.Context, db DBTX, tenantID, memberID, sessionID uuid.UUID, now time.Time) error {
	var revoked *time.Time
	err := db.QueryRow(ctx, `SELECT revoked_at FROM metadata.sessions WHERE id = $1 AND tenant_id = $2 AND member_id = $3`,
		sessionID, tenantID, memberID).Scan(&revoked)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrSessionRevoked
	}
	if err != nil {
		return err
	}
	if revoked != nil {
		return ErrSessionRevoked
	}
	_, err = db.Exec(ctx, `UPDATE metadata.sessions SET last_seen_at = $2 WHERE id = $1 AND last_seen_at < $3`,
		sessionID, now, now.Add(-SessionTouchEvery))
	return err
}

// ExtendSession moves a session's expiry when its token was renewed.
func ExtendSession(ctx context.Context, db DBTX, sessionID uuid.UUID, ttl time.Duration, now time.Time) error {
	_, err := db.Exec(ctx, `UPDATE metadata.sessions SET expires_at = $2 WHERE id = $1 AND revoked_at IS NULL`, sessionID, now.Add(ttl))
	return err
}

// ListSessions returns the identity's live sessions (not revoked, not past expiry), newest first.
func ListSessions(ctx context.Context, db DBTX, identityID uuid.UUID, now time.Time) ([]Session, error) {
	rows, err := db.Query(ctx, `SELECT s.id, s.identity_id, s.member_id, s.tenant_id, coalesce(t.name, ''), s.created_at,
		s.last_seen_at, s.expires_at, coalesce(s.ip, ''), coalesce(s.user_agent, '')
		FROM metadata.sessions s LEFT JOIN control.tenants t ON t.id = s.tenant_id
		WHERE s.identity_id = $1 AND s.revoked_at IS NULL AND s.expires_at > $2
		ORDER BY s.last_seen_at DESC`, identityID, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Session{}
	for rows.Next() {
		var s Session
		if err := rows.Scan(&s.ID, &s.IdentityID, &s.MemberID, &s.TenantID, &s.TenantName, &s.CreatedAt,
			&s.LastSeenAt, &s.ExpiresAt, &s.IP, &s.UserAgent); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// RevokeSession ends one of the identity's own sessions (ErrNotFound if it is not theirs or is
// already over: a person cannot probe another person's session ids).
func RevokeSession(ctx context.Context, db DBTX, identityID, sessionID uuid.UUID, now time.Time) error {
	tag, err := db.Exec(ctx, `UPDATE metadata.sessions SET revoked_at = $3
		WHERE id = $1 AND identity_id = $2 AND revoked_at IS NULL`, sessionID, identityID, now)
	return expectOne(tag, err)
}

// RevokeOtherSessions ends every live session of the identity except keep, returning how many.
func RevokeOtherSessions(ctx context.Context, db DBTX, identityID, keep uuid.UUID, now time.Time) (int, error) {
	tag, err := db.Exec(ctx, `UPDATE metadata.sessions SET revoked_at = $3
		WHERE identity_id = $1 AND id <> $2 AND revoked_at IS NULL AND expires_at > $3`, identityID, keep, now)
	return int(tag.RowsAffected()), err
}

// PurgeSessions deletes sessions that ended more than a week ago (no token of theirs can still be
// valid by then).
func PurgeSessions(ctx context.Context, db DBTX, now time.Time) error {
	_, err := db.Exec(ctx, `DELETE FROM metadata.sessions WHERE expires_at < $1`, now.Add(-7*24*time.Hour))
	return err
}
