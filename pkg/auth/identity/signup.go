package identity

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Signup tunables.
const (
	SignupTTL            = 24 * time.Hour
	MaxSignupsPerEmailHr = 3
)

// ErrSignupInvalid: unknown, expired or already used signup link.
var ErrSignupInvalid = errors.New("this signup link is invalid or has expired")

// Signup is a pending self-service registration.
type Signup struct {
	Email        string
	TenantName   string
	DisplayName  string
	PasswordHash string

	// What RestoreSignup needs to put the row back.
	tokenHash []byte
	expiresAt time.Time
	createdAt time.Time
}

// CreateSignup records a pending signup and returns the link token. token is "" (no error) when
// the address already asked MaxSignupsPerEmailHr times in the last hour: the caller says nothing
// either way, like a password reset. The password is hashed here and never stored in the clear.
func CreateSignup(ctx context.Context, db DBTX, email, tenantName, displayName, password string, now time.Time) (string, error) {
	email, err := normalizeEmail(email)
	if err != nil {
		return "", err
	}
	tenantName = strings.TrimSpace(tenantName)
	hash, err := HashPassword(password)
	if err != nil {
		return "", err
	}
	// The count and the insert are not locked together: a burst may squeeze one extra link
	// through, which only costs one more email to the address's owner.
	var n int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM metadata.signups WHERE lower(email) = lower($1) AND created_at > $2`,
		email, now.Add(-time.Hour)).Scan(&n); err != nil {
		return "", err
	}
	if n >= MaxSignupsPerEmailHr {
		return "", nil
	}
	token, tokenHash, err := newToken()
	if err != nil {
		return "", err
	}
	var display *string
	if d := strings.TrimSpace(displayName); d != "" {
		display = &d
	}
	if _, err := db.Exec(ctx, `INSERT INTO metadata.signups (token_hash, email, tenant_name, display_name, password_hash, expires_at, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`, tokenHash, email, tenantName, display, hash, now.Add(SignupTTL), now); err != nil {
		return "", err
	}
	return token, nil
}

// ConsumeSignup returns the pending signup behind token and removes it (single use), so of two
// requests with the same link only one gets it. It is its own statement: the caller holds no
// connection while it creates the tenant, and puts the row back with RestoreSignup when that fails
// so the link stays usable. (Holding a transaction open around the whole registration took a
// second pooled connection per request, which deadlocks the pool when that many requests arrive
// together.)
func ConsumeSignup(ctx context.Context, db DBTX, token string, now time.Time) (*Signup, error) {
	var s Signup
	var display *string
	s.tokenHash = hashToken(token)
	err := db.QueryRow(ctx, `DELETE FROM metadata.signups WHERE token_hash = $1 AND expires_at > $2
		RETURNING email, tenant_name, display_name, password_hash, expires_at, created_at`, s.tokenHash, now).
		Scan(&s.Email, &s.TenantName, &display, &s.PasswordHash, &s.expiresAt, &s.createdAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrSignupInvalid
	}
	if err != nil {
		return nil, err
	}
	if display != nil {
		s.DisplayName = *display
	}
	return &s, nil
}

// RestoreSignup puts a consumed signup back after the registration it was consumed for failed, so
// the emailed link still works. A signup that was already put back (or a link used twice) is left
// as it is.
func RestoreSignup(ctx context.Context, db DBTX, s *Signup) error {
	var display *string
	if s.DisplayName != "" {
		display = &s.DisplayName
	}
	_, err := db.Exec(ctx, `INSERT INTO metadata.signups (token_hash, email, tenant_name, display_name, password_hash, expires_at, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7) ON CONFLICT (token_hash) DO NOTHING`,
		s.tokenHash, s.Email, s.TenantName, display, s.PasswordHash, s.expiresAt, s.createdAt)
	return err
}

// PurgeSignups drops expired pending signups.
func PurgeSignups(ctx context.Context, db DBTX, now time.Time) error {
	_, err := db.Exec(ctx, `DELETE FROM metadata.signups WHERE expires_at < $1`, now)
	return err
}

// NormalizeEmail trims and validates an address (ErrInvalidEmail).
func NormalizeEmail(email string) (string, error) { return normalizeEmail(email) }
