// Package identity stores who can sign in and where (roadmap 113). An identity is a person
// (email + password); a member is that person inside one tenant (metadata.users — its id is the
// JWT subject, user_roles.user_id and the audit actor). One identity may be a member of several
// tenants.
//
// Every function here takes the shared database (a pool or a transaction on it), never a
// tenant-routed transaction: login looks an identity up before it knows the tenant, and a
// dedicated-database tenant's members live in the shared database too. Roles stay in user_roles,
// reached through control.Router.
package identity

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// DBTX is the subset of pgxpool.Pool / pgx.Tx this package needs.
type DBTX interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Status values. An identity is active or disabled (platform-wide); a member is also "invited"
// until the person accepts (roadmap 113 P2).
const (
	StatusActive   = "active"
	StatusDisabled = "disabled"
	StatusInvited  = "invited"
)

// Lockout: MaxFailedLogins consecutive wrong passwords lock the identity for LockoutDuration.
const (
	MaxFailedLogins = 5
	LockoutDuration = 15 * time.Minute
)

// ServicePrincipalProvider marks the member an OAuth2 client-credentials client acts as.
const ServicePrincipalProvider = "oauth2_client_credentials"

// MinPasswordLength is the platform-wide minimum, which ValidatePasswordLength never goes below.
const MinPasswordLength = 8

var (
	// ErrInvalidCredentials: unknown email, wrong password, or an identity without a password.
	ErrInvalidCredentials = errors.New("invalid email or password")
	// ErrLocked is only returned to somebody already signed in (VerifyCurrentPassword), whatever the
	// password: signing in answers ErrInvalidCredentials while locked, so a locked account cannot be
	// told apart from a wrong password, and the lock never says which password is right.
	ErrLocked = errors.New("too many failed sign-in attempts, try again later")
	// ErrDisabled: the identity or the member is disabled.
	ErrDisabled = errors.New("this account is disabled")
	// ErrSessionRevoked: the token was issued before its sessions were revoked.
	ErrSessionRevoked = errors.New("session was revoked, sign in again")
	ErrAlreadyMember  = errors.New("this person is already a member of the tenant")
	ErrNotFound       = errors.New("user not found")
	ErrWeakPassword   = errors.New("password must be at least 8 characters")
	ErrInvalidEmail   = errors.New("a valid email is required")
)

// Identity is a person, across tenants.
type Identity struct {
	ID           uuid.UUID
	Email        string
	Status       string
	LastTenantID *uuid.UUID
}

// Member is an identity inside one tenant.
type Member struct {
	ID           uuid.UUID
	TenantID     uuid.UUID
	IdentityID   uuid.UUID
	Email        string
	DisplayName  string
	Status       string
	AuthProvider string
	HasPassword  bool
	LastLoginAt  *time.Time
	InvitedBy    *uuid.UUID
	CreatedAt    time.Time
}

// Membership is one tenant an identity can sign in to.
type Membership struct {
	MemberID   uuid.UUID
	TenantID   uuid.UUID
	TenantName string
}

func normalizeEmail(email string) (string, error) {
	email = strings.TrimSpace(email)
	at := strings.LastIndex(email, "@")
	if at <= 0 || at == len(email)-1 || len(email) > 320 || strings.ContainsAny(email, " \t\r\n") {
		return "", ErrInvalidEmail
	}
	return email, nil
}

// WeakPasswordError is a password shorter than the policy (errors.Is(err, ErrWeakPassword)).
type WeakPasswordError struct{ Min int }

func (e *WeakPasswordError) Error() string {
	return fmt.Sprintf("password must be at least %d characters", e.Min)
}

func (e *WeakPasswordError) Is(target error) bool { return target == ErrWeakPassword }

// ValidatePasswordLength checks password against a minimum length (never below MinPasswordLength).
func ValidatePasswordLength(password string, min int) error {
	if min < MinPasswordLength {
		min = MinPasswordLength
	}
	if len([]rune(password)) < min {
		return &WeakPasswordError{Min: min}
	}
	return nil
}

// Authenticate checks email + password and records the outcome (failed-attempt counter, lockout,
// last sign-in). A missing account pays the same hashing cost as a wrong password.
func Authenticate(ctx context.Context, db DBTX, email, password string, now time.Time) (*Identity, error) {
	var (
		id           uuid.UUID
		storedEmail  string
		hash         *string
		status       string
		lastTenantID *uuid.UUID
	)
	err := db.QueryRow(ctx, `SELECT id, email, password_hash, status, last_tenant_id
		FROM metadata.identities WHERE lower(email) = lower($1)`, strings.TrimSpace(email)).
		Scan(&id, &storedEmail, &hash, &status, &lastTenantID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && hash == nil) {
		verifyDummyPassword(password)
		return nil, ErrInvalidCredentials
	}
	if err != nil {
		return nil, err
	}
	claimed, err := claimAttempt(ctx, db, id, now)
	if err != nil {
		return nil, err
	}
	if !claimed {
		verifyDummyPassword(password)
		return nil, ErrInvalidCredentials
	}
	ok, err := VerifyPassword(password, *hash)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrInvalidCredentials
	}
	if status != StatusActive {
		if err := clearAttempts(ctx, db, id); err != nil {
			return nil, err
		}
		return nil, ErrDisabled
	}
	if _, err := db.Exec(ctx, `UPDATE metadata.identities
		SET failed_logins = 0, locked_until = NULL, last_login_at = $2 WHERE id = $1`, id, now); err != nil {
		return nil, err
	}
	return &Identity{ID: id, Email: storedEmail, Status: status, LastTenantID: lastTenantID}, nil
}

// claimAttempt counts one password attempt before the password is looked at, in one statement, so
// attempts sent together are limited like attempts sent one after another. The count assumes the
// attempt fails (a right password clears it with clearAttempts); the one that reaches
// MaxFailedLogins locks the identity. false: it is locked, and the password must not be judged.
func claimAttempt(ctx context.Context, db DBTX, id uuid.UUID, now time.Time) (bool, error) {
	tag, err := db.Exec(ctx, `UPDATE metadata.identities SET
			failed_logins = CASE WHEN failed_logins + 1 >= $3 THEN 0 ELSE failed_logins + 1 END,
			locked_until = CASE WHEN failed_logins + 1 >= $3 THEN $4::timestamptz ELSE locked_until END
		WHERE id = $1 AND (locked_until IS NULL OR locked_until <= $2)`,
		id, now, MaxFailedLogins, now.Add(LockoutDuration))
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// clearAttempts forgets the failed attempts (and the lock the claimed attempt itself may have set)
// once a password proved right.
func clearAttempts(ctx context.Context, db DBTX, id uuid.UUID) error {
	_, err := db.Exec(ctx, `UPDATE metadata.identities SET failed_logins = 0, locked_until = NULL WHERE id = $1`, id)
	return err
}

// Memberships lists the tenants identityID can sign in to right now: active, not removed, in an
// active tenant. A tenant with no control.tenants row (the platform tenant, or one Router serves
// through its unregistered-tenant fallback) counts as active, as it does for Router.
func Memberships(ctx context.Context, db DBTX, identityID uuid.UUID) ([]Membership, error) {
	rows, err := db.Query(ctx, `SELECT u.id, u.tenant_id, coalesce(t.name, '')
		FROM metadata.users u
		LEFT JOIN control.tenants t ON t.id = u.tenant_id
		WHERE u.identity_id = $1 AND u.deleted_at IS NULL AND u.status = 'active'
		  AND (t.id IS NULL OR t.status = 'active')
		ORDER BY coalesce(t.name, ''), u.tenant_id`, identityID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Membership, 0)
	for rows.Next() {
		var m Membership
		if err := rows.Scan(&m.MemberID, &m.TenantID, &m.TenantName); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// RecordSignIn remembers the tenant identityID signed in to (the default next time).
func RecordSignIn(ctx context.Context, db DBTX, identityID, memberID, tenantID uuid.UUID, now time.Time) error {
	if _, err := db.Exec(ctx, `UPDATE metadata.identities SET last_tenant_id = $2 WHERE id = $1`, identityID, tenantID); err != nil {
		return err
	}
	_, err := db.Exec(ctx, `UPDATE metadata.users SET last_login_at = $2 WHERE id = $1`, memberID, now)
	return err
}

// IdentityOf returns the identity behind a member, or ErrNotFound.
func IdentityOf(ctx context.Context, db DBTX, tenantID, memberID uuid.UUID) (uuid.UUID, error) {
	var id uuid.UUID
	err := db.QueryRow(ctx, `SELECT identity_id FROM metadata.users
		WHERE id = $1 AND tenant_id = $2 AND deleted_at IS NULL`, memberID, tenantID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrNotFound
	}
	return id, err
}

// EnsureIdentity returns the identity for email, creating it (with password, when given) if
// there is none. An existing identity's password is never touched: adding someone to a second
// tenant must not let that tenant's admin set their password.
func EnsureIdentity(ctx context.Context, db DBTX, email, password string) (id uuid.UUID, created bool, err error) {
	var hash string
	if password != "" {
		if hash, err = HashPassword(password); err != nil {
			return uuid.Nil, false, err
		}
	}
	return ensureIdentityWithHash(ctx, db, email, hash)
}

// ensureIdentityWithHash is EnsureIdentity for a password that is already hashed ("" = none).
func ensureIdentityWithHash(ctx context.Context, db DBTX, email, passwordHash string) (id uuid.UUID, created bool, err error) {
	email, err = normalizeEmail(email)
	if err != nil {
		return uuid.Nil, false, err
	}
	err = db.QueryRow(ctx, `SELECT id FROM metadata.identities WHERE lower(email) = lower($1)`, email).Scan(&id)
	if err == nil {
		return id, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, err
	}
	var hash *string
	if passwordHash != "" {
		hash = &passwordHash
	}
	err = db.QueryRow(ctx, `INSERT INTO metadata.identities (email, password_hash) VALUES ($1, $2)
		ON CONFLICT (lower(email)) DO NOTHING RETURNING id`, email, hash).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) { // lost a race with a concurrent insert
		err = db.QueryRow(ctx, `SELECT id FROM metadata.identities WHERE lower(email) = lower($1)`, email).Scan(&id)
		return id, false, err
	}
	return id, err == nil, err
}

// NewMember is what AddMember needs to add someone to a tenant.
type NewMember struct {
	TenantID uuid.UUID
	Email    string
	Password string // used only when the identity is new
	// PasswordHash is Password already hashed (signup keeps only the hash); used when Password is empty.
	PasswordHash string
	DisplayName  string
	InvitedBy    *uuid.UUID
	// AuthProvider/ExternalSubject: set for a member provisioned by an external IdP.
	AuthProvider    string
	ExternalSubject string
	// Status: "" (active) or StatusInvited for a member who has to accept an invitation first.
	Status string
}

// AddMember adds a person to a tenant, creating their identity if needed. createdIdentity tells
// the caller whether the password it passed was used.
func AddMember(ctx context.Context, db DBTX, in NewMember) (m *Member, createdIdentity bool, err error) {
	var identityID uuid.UUID
	var created bool
	if in.Password == "" && in.PasswordHash != "" {
		identityID, created, err = ensureIdentityWithHash(ctx, db, in.Email, in.PasswordHash)
	} else {
		identityID, created, err = EnsureIdentity(ctx, db, in.Email, in.Password)
	}
	if err != nil {
		return nil, false, err
	}
	provider := in.AuthProvider
	if provider == "" {
		provider = "local"
	}
	var subject *string
	if in.ExternalSubject != "" {
		subject = &in.ExternalSubject
	}
	var display *string
	if d := strings.TrimSpace(in.DisplayName); d != "" {
		display = &d
	}
	email, _ := normalizeEmail(in.Email)
	status := in.Status
	if status == "" {
		status = StatusActive
	}
	var id uuid.UUID
	err = db.QueryRow(ctx, `INSERT INTO metadata.users
		(tenant_id, email, identity_id, display_name, invited_by, auth_provider, external_subject, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id`,
		in.TenantID, email, identityID, display, in.InvitedBy, provider, subject, status).Scan(&id)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, false, ErrAlreadyMember
		}
		return nil, false, err
	}
	m, err = GetMember(ctx, db, in.TenantID, id)
	return m, created, err
}

const memberColumns = `u.id, u.tenant_id, u.identity_id, i.email, coalesce(u.display_name, ''), u.status,
	u.auth_provider, i.password_hash IS NOT NULL, u.last_login_at, u.invited_by, u.created_at`

func scanMember(row pgx.Row) (*Member, error) {
	var m Member
	if err := row.Scan(&m.ID, &m.TenantID, &m.IdentityID, &m.Email, &m.DisplayName, &m.Status,
		&m.AuthProvider, &m.HasPassword, &m.LastLoginAt, &m.InvitedBy, &m.CreatedAt); err != nil {
		return nil, err
	}
	return &m, nil
}

// GetMember returns one (not removed) member of tenantID, or ErrNotFound.
func GetMember(ctx context.Context, db DBTX, tenantID, memberID uuid.UUID) (*Member, error) {
	m, err := scanMember(db.QueryRow(ctx, `SELECT `+memberColumns+`
		FROM metadata.users u JOIN metadata.identities i ON i.id = u.identity_id
		WHERE u.id = $1 AND u.tenant_id = $2 AND u.deleted_at IS NULL`, memberID, tenantID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return m, err
}

// FindExternalMember finds the member a tenant's external IdP provisioned for subject.
func FindExternalMember(ctx context.Context, db DBTX, tenantID uuid.UUID, provider, subject string) (*Member, error) {
	m, err := scanMember(db.QueryRow(ctx, `SELECT `+memberColumns+`
		FROM metadata.users u JOIN metadata.identities i ON i.id = u.identity_id
		WHERE u.tenant_id = $1 AND u.auth_provider = $2 AND u.external_subject = $3 AND u.deleted_at IS NULL`,
		tenantID, provider, subject))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return m, err
}

// ListQuery filters and pages ListMembers.
type ListQuery struct {
	Search string // email or display name, case-insensitive substring
	Status string // "" = any
	// IDs, when non-nil, restricts to these members (the caller resolved a role filter).
	IDs    []uuid.UUID
	Offset int
	Limit  int
}

// MaxListLimit caps one page.
const MaxListLimit = 200

// ListMembers returns one page of tenantID's members, ordered by email, and the total matching.
func ListMembers(ctx context.Context, db DBTX, tenantID uuid.UUID, q ListQuery) ([]Member, int, error) {
	if q.Limit <= 0 || q.Limit > MaxListLimit {
		q.Limit = 50
	}
	if q.Offset < 0 {
		q.Offset = 0
	}
	// OAuth client service principals are members too (they need roles) but are managed with
	// their client, not listed as people.
	where := `u.tenant_id = $1 AND u.deleted_at IS NULL AND u.auth_provider NOT IN ('` + ServicePrincipalProvider + `', '` + ServiceAccountProvider + `')`
	args := []any{tenantID}
	if s := strings.TrimSpace(q.Search); s != "" {
		args = append(args, "%"+escapeLike(s)+"%")
		where += ` AND (i.email ILIKE $` + strconv.Itoa(len(args)) + ` OR u.display_name ILIKE $` + strconv.Itoa(len(args)) + `)`
	}
	if q.Status != "" {
		args = append(args, q.Status)
		where += ` AND u.status = $` + strconv.Itoa(len(args))
	}
	if q.IDs != nil {
		args = append(args, q.IDs)
		where += ` AND u.id = ANY($` + strconv.Itoa(len(args)) + `)`
	}
	from := ` FROM metadata.users u JOIN metadata.identities i ON i.id = u.identity_id WHERE ` + where
	var total int
	if err := db.QueryRow(ctx, `SELECT count(*)`+from, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, q.Limit, q.Offset)
	rows, err := db.Query(ctx, `SELECT `+memberColumns+from+` ORDER BY lower(i.email)
		LIMIT $`+strconv.Itoa(len(args)-1)+` OFFSET $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := make([]Member, 0)
	for rows.Next() {
		m, err := scanMember(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *m)
	}
	return out, total, rows.Err()
}

// UpdateMember changes a member's display name.
func UpdateMember(ctx context.Context, db DBTX, tenantID, memberID uuid.UUID, displayName string) error {
	var display *string
	if d := strings.TrimSpace(displayName); d != "" {
		display = &d
	}
	return expectOne(db.Exec(ctx, `UPDATE metadata.users SET display_name = $3, updated_at = now()
		WHERE id = $1 AND tenant_id = $2 AND deleted_at IS NULL`, memberID, tenantID, display))
}

// SetMemberStatus enables or disables a member. Disabling also revokes their sessions in this
// tenant.
func SetMemberStatus(ctx context.Context, db DBTX, tenantID, memberID uuid.UUID, status string, now time.Time) error {
	if status != StatusActive && status != StatusDisabled {
		return errors.New("status must be active or disabled")
	}
	return expectOne(db.Exec(ctx, `UPDATE metadata.users
		SET status = $3, updated_at = now(),
		    sessions_revoked_at = CASE WHEN $3 = 'disabled' THEN $4 ELSE sessions_revoked_at END
		WHERE id = $1 AND tenant_id = $2 AND deleted_at IS NULL`, memberID, tenantID, status, now))
}

// RemoveMember removes a member from the tenant (soft delete, sessions revoked, any open
// invitation revoked). The identity and its other memberships are untouched.
func RemoveMember(ctx context.Context, db DBTX, tenantID, memberID uuid.UUID, now time.Time) error {
	if err := expectOne(db.Exec(ctx, `UPDATE metadata.users
		SET deleted_at = $3, status = 'disabled', sessions_revoked_at = $3, updated_at = now()
		WHERE id = $1 AND tenant_id = $2 AND deleted_at IS NULL`, memberID, tenantID, now)); err != nil {
		return err
	}
	return RevokeInvitations(ctx, db, memberID, now)
}

// SetPassword replaces an identity's password and revokes all of its sessions everywhere.
func SetPassword(ctx context.Context, db DBTX, identityID uuid.UUID, password string, now time.Time) error {
	hash, err := HashPassword(password)
	if err != nil {
		return err
	}
	return expectOne(db.Exec(ctx, `UPDATE metadata.identities
		SET password_hash = $2, sessions_revoked_at = $3, failed_logins = 0, locked_until = NULL, updated_at = now()
		WHERE id = $1`, identityID, hash, now))
}

// CheckPassword reports whether password is identityID's current password, without counting a
// failure: for tests and tools. A request path must use VerifyCurrentPassword (or Authenticate).
func CheckPassword(ctx context.Context, db DBTX, identityID uuid.UUID, password string) (bool, error) {
	var hash *string
	if err := db.QueryRow(ctx, `SELECT password_hash FROM metadata.identities WHERE id = $1`, identityID).Scan(&hash); err != nil {
		return false, err
	}
	if hash == nil {
		return false, nil
	}
	return VerifyPassword(password, *hash)
}

// VerifyCurrentPassword checks that password is identityID's current one, for an action by a
// person who is already signed in (changing the password). It counts a wrong password toward the
// same lockout as signing in (ErrInvalidCredentials, then ErrLocked once locked), so a stolen
// session cannot be used to guess the password without limit.
func VerifyCurrentPassword(ctx context.Context, db DBTX, identityID uuid.UUID, password string, now time.Time) error {
	var hash *string
	if err := db.QueryRow(ctx, `SELECT password_hash FROM metadata.identities WHERE id = $1`, identityID).Scan(&hash); err != nil {
		return err
	}
	if hash == nil {
		verifyDummyPassword(password)
		return ErrInvalidCredentials
	}
	claimed, err := claimAttempt(ctx, db, identityID, now)
	if err != nil {
		return err
	}
	if !claimed {
		return ErrLocked
	}
	ok, err := VerifyPassword(password, *hash)
	if err != nil {
		return err
	}
	if !ok {
		return ErrInvalidCredentials
	}
	return clearAttempts(ctx, db, identityID)
}

// CheckSession decides whether a token for (tenantID, userID), issued at issuedAt, may still be
// used. A subject with no member row is not a person (a service principal) and passes.
func CheckSession(ctx context.Context, db DBTX, tenantID, userID uuid.UUID, issuedAt *time.Time) error {
	var (
		memberStatus, identityStatus string
		deletedAt                    *time.Time
		memberRevoked, identRevoked  *time.Time
	)
	err := db.QueryRow(ctx, `SELECT u.status, u.deleted_at, u.sessions_revoked_at, i.status, i.sessions_revoked_at
		FROM metadata.users u JOIN metadata.identities i ON i.id = u.identity_id
		WHERE u.id = $1 AND u.tenant_id = $2`, userID, tenantID).
		Scan(&memberStatus, &deletedAt, &memberRevoked, &identityStatus, &identRevoked)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if deletedAt != nil || memberStatus == StatusDisabled || identityStatus != StatusActive {
		return ErrDisabled
	}
	for _, revoked := range []*time.Time{memberRevoked, identRevoked} {
		if revoked == nil {
			continue
		}
		// iat has whole-second precision: a token minted in the same second, after the
		// revocation (the fresh session a password change hands back), must survive it.
		if issuedAt == nil || issuedAt.Before(revoked.Truncate(time.Second)) {
			return ErrSessionRevoked
		}
	}
	return nil
}

func expectOne(tag pgconn.CommandTag, err error) error {
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// PickMembership chooses which membership a sign-in lands in: the one in tenantID when given,
// else the only one, else the last used one (lastTenantID). ok is false when none fits — no
// membership, tenantID not among them, or several with none remembered.
func PickMembership(ms []Membership, tenantID, lastTenantID *uuid.UUID) (Membership, bool) {
	if tenantID != nil {
		for _, m := range ms {
			if m.TenantID == *tenantID {
				return m, true
			}
		}
		return Membership{}, false
	}
	if len(ms) == 1 {
		return ms[0], true
	}
	if lastTenantID != nil {
		for _, m := range ms {
			if m.TenantID == *lastTenantID {
				return m, true
			}
		}
	}
	return Membership{}, false
}

// SharedIdentities reports which of identityIDs also belong to a tenant other than tenantID
// (removed memberships excluded). Their password is not tenantID's admin's to reset.
func SharedIdentities(ctx context.Context, db DBTX, tenantID uuid.UUID, identityIDs []uuid.UUID) (map[uuid.UUID]bool, error) {
	rows, err := db.Query(ctx, `SELECT DISTINCT identity_id FROM metadata.users
		WHERE identity_id = ANY($1) AND tenant_id <> $2 AND deleted_at IS NULL`, identityIDs, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[uuid.UUID]bool{}
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}
