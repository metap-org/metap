package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Invitations and password-reset links (roadmap 113, P2). A token is 32 random bytes, sent only by
// email; the database keeps its SHA-256, so a read of the table can't be turned into a sign-in.

const (
	// InvitationTTL is how long an invitation link works.
	InvitationTTL = 7 * 24 * time.Hour
	// PasswordResetTTL is how long a reset link works.
	PasswordResetTTL = time.Hour
	// MaxPasswordResetsPerHour caps reset emails per identity, so "forgot password" can't be used to
	// flood someone's inbox.
	MaxPasswordResetsPerHour = 3
)

var (
	// ErrInvitationInvalid: unknown, expired, already used or revoked — deliberately one error, so
	// the accept page can't be used to probe which.
	ErrInvitationInvalid = errors.New("this invitation link is invalid or has expired")
	// ErrResetInvalid: unknown, expired or already used reset link.
	ErrResetInvalid = errors.New("this password reset link is invalid or has expired")
	// ErrInvitationNeedsSignIn: the invited person already has an account without a password (they
	// sign in through their tenant's identity provider), so a link alone can't prove it is them.
	ErrInvitationNeedsSignIn = errors.New("this account signs in with an identity provider; sign in there to join")
)

// newToken returns a random URL-safe token and the hash to store.
func newToken() (string, []byte, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", nil, err
	}
	token := base64.RawURLEncoding.EncodeToString(b)
	return token, hashToken(token), nil
}

func hashToken(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}

// Invitation is a pending (or looked-up) invitation, with what the accept page shows.
type Invitation struct {
	ID          uuid.UUID
	TenantID    uuid.UUID
	TenantName  string
	MemberID    uuid.UUID
	IdentityID  uuid.UUID
	Email       string
	DisplayName string
	// NewIdentity: the account was created by this invitation — accepting it sets the password.
	NewIdentity bool
	// HasPassword: the account already has a password, which accepting must confirm.
	HasPassword bool
	InvitedBy   *uuid.UUID
	CreatedAt   time.Time
	ExpiresAt   time.Time
}

// CreateInvitation issues a fresh invitation for an invited member, revoking any earlier one (a
// resend makes the old link stop working). Returns the token to email.
func CreateInvitation(ctx context.Context, db DBTX, tenantID, memberID, identityID uuid.UUID, newIdentity bool,
	invitedBy *uuid.UUID, now time.Time) (string, *Invitation, error) {
	if err := RevokeInvitations(ctx, db, memberID, now); err != nil {
		return "", nil, err
	}
	token, hash, err := newToken()
	if err != nil {
		return "", nil, err
	}
	var id uuid.UUID
	expires := now.Add(InvitationTTL)
	if err := db.QueryRow(ctx, `INSERT INTO metadata.invitations
		(tenant_id, member_id, identity_id, new_identity, token_hash, invited_by, created_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id`,
		tenantID, memberID, identityID, newIdentity, hash, invitedBy, now, expires).Scan(&id); err != nil {
		return "", nil, err
	}
	inv, err := invitationByID(ctx, db, id)
	return token, inv, err
}

const invitationSelect = `SELECT v.id, v.tenant_id, coalesce(t.name, ''), v.member_id, v.identity_id, i.email,
	coalesce(u.display_name, ''), v.new_identity, i.password_hash IS NOT NULL, v.invited_by, v.created_at, v.expires_at
	FROM metadata.invitations v
	JOIN metadata.users u ON u.id = v.member_id
	JOIN metadata.identities i ON i.id = v.identity_id
	LEFT JOIN control.tenants t ON t.id = v.tenant_id`

func scanInvitation(row pgx.Row) (*Invitation, error) {
	var inv Invitation
	err := row.Scan(&inv.ID, &inv.TenantID, &inv.TenantName, &inv.MemberID, &inv.IdentityID, &inv.Email,
		&inv.DisplayName, &inv.NewIdentity, &inv.HasPassword, &inv.InvitedBy, &inv.CreatedAt, &inv.ExpiresAt)
	if err != nil {
		return nil, err
	}
	return &inv, nil
}

func invitationByID(ctx context.Context, db DBTX, id uuid.UUID) (*Invitation, error) {
	return scanInvitation(db.QueryRow(ctx, invitationSelect+` WHERE v.id = $1`, id))
}

// FindInvitation returns the usable invitation for token: not expired, accepted or revoked, and
// its member still invited. Anything else is ErrInvitationInvalid.
func FindInvitation(ctx context.Context, db DBTX, token string, now time.Time) (*Invitation, error) {
	inv, err := scanInvitation(db.QueryRow(ctx, invitationSelect+`
		WHERE v.token_hash = $1 AND v.accepted_at IS NULL AND v.revoked_at IS NULL AND v.expires_at > $2
		  AND u.deleted_at IS NULL AND u.status = 'invited' AND i.status = 'active'`, hashToken(token), now))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrInvitationInvalid
	}
	return inv, err
}

// PendingInvitation is memberID's open invitation, or nil.
func PendingInvitation(ctx context.Context, db DBTX, memberID uuid.UUID, now time.Time) (*Invitation, error) {
	inv, err := scanInvitation(db.QueryRow(ctx, invitationSelect+`
		WHERE v.member_id = $1 AND v.accepted_at IS NULL AND v.revoked_at IS NULL AND v.expires_at > $2
		ORDER BY v.created_at DESC LIMIT 1`, memberID, now))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return inv, err
}

// RevokeInvitations makes every open invitation of memberID stop working.
func RevokeInvitations(ctx context.Context, db DBTX, memberID uuid.UUID, now time.Time) error {
	_, err := db.Exec(ctx, `UPDATE metadata.invitations SET revoked_at = $2
		WHERE member_id = $1 AND accepted_at IS NULL AND revoked_at IS NULL`, memberID, now)
	return err
}

// AcceptInvitation marks inv used and activates its member. Exactly-once: a second accept of the
// same invitation (a double submit, a race) gets ErrInvitationInvalid.
func AcceptInvitation(ctx context.Context, db DBTX, inv *Invitation, now time.Time) error {
	tag, err := db.Exec(ctx, `UPDATE metadata.invitations SET accepted_at = $2
		WHERE id = $1 AND accepted_at IS NULL AND revoked_at IS NULL AND expires_at > $2`, inv.ID, now)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrInvitationInvalid
	}
	tag, err = db.Exec(ctx, `UPDATE metadata.users SET status = 'active', updated_at = now()
		WHERE id = $1 AND status = 'invited' AND deleted_at IS NULL`, inv.MemberID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrInvitationInvalid
	}
	return nil
}

// FindIdentityByEmail returns the active identity for email, or ErrNotFound.
func FindIdentityByEmail(ctx context.Context, db DBTX, email string) (*Identity, error) {
	email, err := normalizeEmail(email)
	if err != nil {
		return nil, ErrNotFound
	}
	var ident Identity
	err = db.QueryRow(ctx, `SELECT id, email, status, last_tenant_id FROM metadata.identities
		WHERE lower(email) = lower($1)`, email).Scan(&ident.ID, &ident.Email, &ident.Status, &ident.LastTenantID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &ident, nil
}

// CreatePasswordReset issues a reset link for identityID unless MaxPasswordResetsPerHour were
// already issued in the last hour (then token is "" and err nil: the caller says nothing either way).
// The count and the insert run under a per-identity advisory lock, so concurrent requests can't
// slip past the limit together.
func CreatePasswordReset(ctx context.Context, db DBTX, identityID uuid.UUID, now time.Time) (string, error) {
	b, ok := db.(beginner)
	if !ok {
		return "", errors.New("identity: CreatePasswordReset needs a pool or a transaction")
	}
	tx, err := b.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('password_reset:' || $1::text, 0))`, identityID); err != nil {
		return "", err
	}
	var recent int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM metadata.password_resets
		WHERE identity_id = $1 AND created_at > $2`, identityID, now.Add(-time.Hour)).Scan(&recent); err != nil {
		return "", err
	}
	if recent >= MaxPasswordResetsPerHour {
		return "", nil
	}
	token, hash, err := newToken()
	if err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO metadata.password_resets (identity_id, token_hash, created_at, expires_at)
		VALUES ($1, $2, $3, $4)`, identityID, hash, now, now.Add(PasswordResetTTL)); err != nil {
		return "", err
	}
	return token, tx.Commit(ctx)
}

// beginner is a pool or a transaction (a nested Begin is a savepoint).
type beginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// ResetPasswordWithToken sets a new password through a reset link: the link is used up (and every
// other open link of the identity with it), the password replaced, every session revoked and the
// lockout cleared. validate checks the new password against the identity's policy first.
func ResetPasswordWithToken(ctx context.Context, db DBTX, token, password string, now time.Time,
	validate func(identityID uuid.UUID) error) (uuid.UUID, error) {
	var id, identityID uuid.UUID
	err := db.QueryRow(ctx, `SELECT r.id, r.identity_id FROM metadata.password_resets r
		JOIN metadata.identities i ON i.id = r.identity_id
		WHERE r.token_hash = $1 AND r.used_at IS NULL AND r.expires_at > $2 AND i.status = 'active'`,
		hashToken(token), now).Scan(&id, &identityID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrResetInvalid
	}
	if err != nil {
		return uuid.Nil, err
	}
	if err := validate(identityID); err != nil {
		return uuid.Nil, err
	}
	tag, err := db.Exec(ctx, `UPDATE metadata.password_resets SET used_at = $2
		WHERE identity_id = $1 AND used_at IS NULL`, identityID, now)
	if err != nil {
		return uuid.Nil, err
	}
	if tag.RowsAffected() == 0 {
		return uuid.Nil, ErrResetInvalid
	}
	return identityID, SetPassword(ctx, db, identityID, password, now)
}

// IdentityTenants is every tenant identityID is (or is invited to be) a member of.
func IdentityTenants(ctx context.Context, db DBTX, identityID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := db.Query(ctx, `SELECT DISTINCT tenant_id FROM metadata.users
		WHERE identity_id = $1 AND deleted_at IS NULL`, identityID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]uuid.UUID, 0)
	for rows.Next() {
		var t uuid.UUID
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// PasswordPolicy resolves one tenant's minimum password length (its auth.passwordMinLength).
type PasswordPolicy func(ctx context.Context, tenantID uuid.UUID) int

// RequiredPasswordLength is the strictest minimum among the identity's tenants plus extra (the one
// being joined): one password serves every tenant, so it must satisfy all of them. identityID may
// be uuid.Nil (no account yet); policy nil means the platform minimum.
func RequiredPasswordLength(ctx context.Context, db DBTX, identityID uuid.UUID, policy PasswordPolicy, extra ...uuid.UUID) (int, error) {
	min := MinPasswordLength
	if policy == nil {
		return min, nil
	}
	tenants := append([]uuid.UUID{}, extra...)
	if identityID != uuid.Nil {
		own, err := IdentityTenants(ctx, db, identityID)
		if err != nil {
			return 0, err
		}
		tenants = append(tenants, own...)
	}
	seen := map[uuid.UUID]bool{}
	for _, t := range tenants {
		if seen[t] {
			continue
		}
		seen[t] = true
		if n := policy(ctx, t); n > min {
			min = n
		}
	}
	return min, nil
}

// CheckPasswordPolicy validates password for identityID (and extra tenants) in one call.
func CheckPasswordPolicy(ctx context.Context, db DBTX, identityID uuid.UUID, password string, policy PasswordPolicy, extra ...uuid.UUID) error {
	min, err := RequiredPasswordLength(ctx, db, identityID, policy, extra...)
	if err != nil {
		return err
	}
	return ValidatePasswordLength(password, min)
}

// CanSetPasswordOnAccept reports whether accepting memberID's invitation may set identityID's
// password: only for an account with no password that belongs to no other tenant. Anything else
// would let the inviting tenant's admin, who can see the link, choose the password of someone else's
// account. Checked when the invitation is created and again when it is accepted.
func CanSetPasswordOnAccept(ctx context.Context, db DBTX, identityID, memberID uuid.UUID) (bool, error) {
	var ok bool
	err := db.QueryRow(ctx, `SELECT i.password_hash IS NULL AND NOT EXISTS (
			SELECT 1 FROM metadata.users u
			WHERE u.identity_id = i.id AND u.id <> $2 AND u.deleted_at IS NULL)
		FROM metadata.identities i WHERE i.id = $1`, identityID, memberID).Scan(&ok)
	return ok, err
}
