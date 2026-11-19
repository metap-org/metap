package identity

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // RFC 6238 TOTP is defined over HMAC-SHA1; authenticator apps expect it.
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// MFA errors. ErrMFAInvalidCode covers a wrong, replayed or already-used code alike.
var (
	ErrMFAInvalidCode  = errors.New("invalid verification code")
	ErrMFAChallenge    = errors.New("this sign-in step expired, sign in again")
	ErrMFANotEnrolled  = errors.New("two-factor authentication is not set up")
	ErrMFAAlreadyOn    = errors.New("two-factor authentication is already on")
	ErrMFAEnrollNotRun = errors.New("start the setup first")
)

// MFA tunables.
const (
	totpPeriod       = 30
	totpDigits       = 6
	totpSkewSteps    = 1 // accept one step either side of now (clock drift)
	recoveryCodes    = 10
	ChallengeTTL     = 5 * time.Minute
	MaxChallengeTry  = 5
	ChallengeSignIn  = "signin"
	ChallengeSetup   = "setup"
	totpIssuerPrefix = "Metap"
)

func totpCode(secret []byte, step int64) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(step))
	mac := hmac.New(sha1.New, secret)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	v := (binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff) % 1_000_000
	return fmt.Sprintf("%0*d", totpDigits, v)
}

func decodeSecret(s string) ([]byte, error) {
	return base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(s))
}

// matchTOTP returns the step a code matches within the skew window and after lastUsed, or 0.
func matchTOTP(secret []byte, code string, now time.Time, lastUsed int64) int64 {
	code = strings.TrimSpace(code)
	if len(code) != totpDigits {
		return 0
	}
	cur := now.Unix() / totpPeriod
	for d := int64(-totpSkewSteps); d <= totpSkewSteps; d++ {
		step := cur + d
		if step <= lastUsed {
			continue // a code is single use: replaying one inside its window is refused
		}
		if subtle.ConstantTimeCompare([]byte(totpCode(secret, step)), []byte(code)) == 1 {
			return step
		}
	}
	return 0
}

// MFAEnabled reports whether the identity has a confirmed second factor.
func MFAEnabled(ctx context.Context, db DBTX, identityID uuid.UUID) (bool, error) {
	var ok bool
	err := db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM metadata.mfa_factors
		WHERE identity_id = $1 AND confirmed_at IS NOT NULL)`, identityID).Scan(&ok)
	return ok, err
}

// BeginMFAEnrollment creates (or restarts) an unconfirmed TOTP factor and returns the base32
// secret and the otpauth:// URI an authenticator app scans. A confirmed factor is not replaced:
// turn it off first.
func BeginMFAEnrollment(ctx context.Context, db DBTX, identityID uuid.UUID, account string, now time.Time) (secret, uri string, err error) {
	if on, err := MFAEnabled(ctx, db, identityID); err != nil {
		return "", "", err
	} else if on {
		return "", "", ErrMFAAlreadyOn
	}
	raw := make([]byte, 20)
	if _, err := rand.Read(raw); err != nil {
		return "", "", err
	}
	secret = base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw)
	if _, err := db.Exec(ctx, `INSERT INTO metadata.mfa_factors (identity_id, secret, created_at) VALUES ($1,$2,$3)
		ON CONFLICT (identity_id) DO UPDATE SET secret = EXCLUDED.secret, confirmed_at = NULL, last_used_step = 0,
		created_at = EXCLUDED.created_at`, identityID, secret, now); err != nil {
		return "", "", err
	}
	label := url.PathEscape(totpIssuerPrefix + ":" + account)
	uri = fmt.Sprintf("otpauth://totp/%s?secret=%s&issuer=%s&algorithm=SHA1&digits=%d&period=%d",
		label, secret, url.QueryEscape(totpIssuerPrefix), totpDigits, totpPeriod)
	return secret, uri, nil
}

// ConfirmMFAEnrollment checks the first code from the app, turns the factor on and returns the
// recovery codes — shown once, stored hashed.
func ConfirmMFAEnrollment(ctx context.Context, db DBTX, identityID uuid.UUID, code string, now time.Time) ([]string, error) {
	var secretText string
	var confirmed *time.Time
	err := db.QueryRow(ctx, `SELECT secret, confirmed_at FROM metadata.mfa_factors WHERE identity_id = $1 FOR UPDATE`, identityID).
		Scan(&secretText, &confirmed)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrMFAEnrollNotRun
	}
	if err != nil {
		return nil, err
	}
	if confirmed != nil {
		return nil, ErrMFAAlreadyOn
	}
	secret, err := decodeSecret(secretText)
	if err != nil {
		return nil, err
	}
	step := matchTOTP(secret, code, now, 0)
	if step == 0 {
		return nil, ErrMFAInvalidCode
	}
	if _, err := db.Exec(ctx, `UPDATE metadata.mfa_factors SET confirmed_at = $2, last_used_step = $3 WHERE identity_id = $1`,
		identityID, now, step); err != nil {
		return nil, err
	}
	return replaceRecoveryCodes(ctx, db, identityID)
}

func newRecoveryCode() (string, error) {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	s := strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b)) // 10 chars
	return s[:5] + "-" + s[5:], nil
}

func hashRecoveryCode(code string) []byte {
	code = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(code), " ", ""))
	h := sha256.Sum256([]byte(code))
	return h[:]
}

func replaceRecoveryCodes(ctx context.Context, db DBTX, identityID uuid.UUID) ([]string, error) {
	if _, err := db.Exec(ctx, `DELETE FROM metadata.mfa_recovery_codes WHERE identity_id = $1`, identityID); err != nil {
		return nil, err
	}
	out := make([]string, 0, recoveryCodes)
	for i := 0; i < recoveryCodes; i++ {
		c, err := newRecoveryCode()
		if err != nil {
			return nil, err
		}
		if _, err := db.Exec(ctx, `INSERT INTO metadata.mfa_recovery_codes (identity_id, code_hash) VALUES ($1,$2)
			ON CONFLICT DO NOTHING`, identityID, hashRecoveryCode(c)); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

// RegenerateRecoveryCodes replaces every recovery code (the caller proved a current code first).
func RegenerateRecoveryCodes(ctx context.Context, db DBTX, identityID uuid.UUID) ([]string, error) {
	if on, err := MFAEnabled(ctx, db, identityID); err != nil {
		return nil, err
	} else if !on {
		return nil, ErrMFANotEnrolled
	}
	return replaceRecoveryCodes(ctx, db, identityID)
}

// RecoveryCodesLeft counts the unused recovery codes.
func RecoveryCodesLeft(ctx context.Context, db DBTX, identityID uuid.UUID) (int, error) {
	var n int
	err := db.QueryRow(ctx, `SELECT count(*) FROM metadata.mfa_recovery_codes WHERE identity_id = $1 AND used_at IS NULL`, identityID).Scan(&n)
	return n, err
}

// VerifyMFA accepts a current TOTP code or an unused recovery code (which it then burns). A TOTP
// code is single use: the step it matched is recorded and anything at or before it is refused.
func VerifyMFA(ctx context.Context, db DBTX, identityID uuid.UUID, code string, now time.Time) error {
	var secretText string
	var last int64
	err := db.QueryRow(ctx, `SELECT secret, last_used_step FROM metadata.mfa_factors
		WHERE identity_id = $1 AND confirmed_at IS NOT NULL FOR UPDATE`, identityID).Scan(&secretText, &last)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrMFANotEnrolled
	}
	if err != nil {
		return err
	}
	secret, err := decodeSecret(secretText)
	if err != nil {
		return err
	}
	if step := matchTOTP(secret, code, now, last); step != 0 {
		_, err := db.Exec(ctx, `UPDATE metadata.mfa_factors SET last_used_step = $2 WHERE identity_id = $1`, identityID, step)
		return err
	}
	tag, err := db.Exec(ctx, `UPDATE metadata.mfa_recovery_codes SET used_at = $3
		WHERE identity_id = $1 AND code_hash = $2 AND used_at IS NULL`, identityID, hashRecoveryCode(code), now)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrMFAInvalidCode
	}
	return nil
}

// DisableMFA removes the factor and its recovery codes (the caller proved a code, or an admin
// reset it for a locked-out person).
func DisableMFA(ctx context.Context, db DBTX, identityID uuid.UUID) error {
	if _, err := db.Exec(ctx, `DELETE FROM metadata.mfa_recovery_codes WHERE identity_id = $1`, identityID); err != nil {
		return err
	}
	_, err := db.Exec(ctx, `DELETE FROM metadata.mfa_factors WHERE identity_id = $1`, identityID)
	return err
}

// Challenge is the "password was right" proof between the two sign-in steps.
type Challenge struct {
	IdentityID uuid.UUID
	TenantID   *uuid.UUID
	Purpose    string
}

func challengeHash(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}

// CreateChallenge issues a short-lived single-use token. purpose is ChallengeSignIn (enter a code)
// or ChallengeSetup (the tenant requires MFA and the person has none yet: enrol first).
func CreateChallenge(ctx context.Context, db DBTX, identityID uuid.UUID, tenantID *uuid.UUID, purpose string, now time.Time) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(b)
	_, err := db.Exec(ctx, `INSERT INTO metadata.mfa_challenges (token_hash, identity_id, tenant_id, purpose, expires_at, created_at)
		VALUES ($1,$2,$3,$4,$5,$6)`, challengeHash(token), identityID, tenantID, purpose, now.Add(ChallengeTTL), now)
	return token, err
}

// LookupChallenge returns a live challenge of purpose without using it up.
func LookupChallenge(ctx context.Context, db DBTX, token, purpose string, now time.Time) (*Challenge, error) {
	var c Challenge
	err := db.QueryRow(ctx, `SELECT identity_id, tenant_id, purpose FROM metadata.mfa_challenges
		WHERE token_hash = $1 AND purpose = $2 AND expires_at > $3 AND attempts < $4`,
		challengeHash(token), purpose, now, MaxChallengeTry).Scan(&c.IdentityID, &c.TenantID, &c.Purpose)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrMFAChallenge
	}
	return &c, err
}

// FailChallenge counts a wrong code against the challenge; MaxChallengeTry of them kill it.
func FailChallenge(ctx context.Context, db DBTX, token string) error {
	_, err := db.Exec(ctx, `UPDATE metadata.mfa_challenges SET attempts = attempts + 1 WHERE token_hash = $1`, challengeHash(token))
	return err
}

// ConsumeChallenge uses the challenge up once the second step succeeded.
func ConsumeChallenge(ctx context.Context, db DBTX, token string) error {
	_, err := db.Exec(ctx, `DELETE FROM metadata.mfa_challenges WHERE token_hash = $1`, challengeHash(token))
	return err
}

// PurgeChallenges drops expired challenges (called opportunistically at sign-in).
func PurgeChallenges(ctx context.Context, db DBTX, now time.Time) error {
	_, err := db.Exec(ctx, `DELETE FROM metadata.mfa_challenges WHERE expires_at < $1`, now)
	return err
}

// FindIdentityByID returns the identity, or ErrNotFound.
func FindIdentityByID(ctx context.Context, db DBTX, id uuid.UUID) (*Identity, error) {
	var ident Identity
	err := db.QueryRow(ctx, `SELECT id, email, status, last_tenant_id FROM metadata.identities WHERE id = $1`, id).
		Scan(&ident.ID, &ident.Email, &ident.Status, &ident.LastTenantID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &ident, err
}

// TOTPAt returns the code an authenticator app shows for the base32 secret at time at. Tests and
// operator tooling use it to act as the app.
func TOTPAt(secret string, at time.Time) (string, error) {
	raw, err := decodeSecret(secret)
	if err != nil {
		return "", err
	}
	return totpCode(raw, at.Unix()/totpPeriod), nil
}
