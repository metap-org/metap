package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ServiceAccountProvider marks a member that is a machine, not a person: it has no password, signs
// in only through an API key, and is managed from the service-account screens rather than listed
// among users (roadmap 115 M5).
const ServiceAccountProvider = "service_account"

// APIKeyPrefix starts every API key, so an authenticator can tell one from a JWT without parsing.
const APIKeyPrefix = "mk_"

// ErrAPIKeyInvalid: unknown, revoked or expired key (one error, so a caller learns nothing more).
var ErrAPIKeyInvalid = errors.New("invalid or expired API key")

// APIKey is a key's metadata; the secret itself is never stored.
type APIKey struct {
	ID         uuid.UUID
	TenantID   uuid.UUID
	MemberID   uuid.UUID
	Name       string
	Prefix     string
	ExpiresAt  *time.Time
	RevokedAt  *time.Time
	LastUsedAt *time.Time
	CreatedBy  *uuid.UUID
	CreatedAt  time.Time
}

// IsAPIKey reports whether a bearer token is an API key rather than a JWT.
func IsAPIKey(token string) bool { return strings.HasPrefix(token, APIKeyPrefix) }

func hashAPIKey(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}

// CreateServiceAccount adds a password-less machine member to the tenant. The identity email is
// synthetic and unroutable, so it can never collide with or sign in as a person.
func CreateServiceAccount(ctx context.Context, db DBTX, tenantID uuid.UUID, name string, createdBy *uuid.UUID) (*Member, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 200 {
		return nil, errors.New("a service account name of 1-200 characters is required")
	}
	m, _, err := AddMember(ctx, db, NewMember{
		TenantID:     tenantID,
		Email:        "sa-" + uuid.NewString() + "@service-account.invalid",
		DisplayName:  name,
		InvitedBy:    createdBy,
		AuthProvider: ServiceAccountProvider,
	})
	return m, err
}

// CreateAPIKey issues a key for memberID and returns it once; only its hash is kept. expiresAt nil
// means it does not expire (a deployment may cap that through config).
func CreateAPIKey(ctx context.Context, db DBTX, tenantID, memberID uuid.UUID, name string,
	expiresAt *time.Time, createdBy *uuid.UUID, now time.Time) (string, *APIKey, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 200 {
		return "", nil, errors.New("a key name of 1-200 characters is required")
	}
	if _, err := GetMember(ctx, db, tenantID, memberID); err != nil {
		return "", nil, err
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", nil, err
	}
	token := APIKeyPrefix + base64.RawURLEncoding.EncodeToString(b)
	prefix := token[:len(APIKeyPrefix)+5]
	var k APIKey
	err := db.QueryRow(ctx, `INSERT INTO metadata.api_keys
		(tenant_id, member_id, name, prefix, token_hash, expires_at, created_by, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		RETURNING id, tenant_id, member_id, name, prefix, expires_at, revoked_at, last_used_at, created_by, created_at`,
		tenantID, memberID, name, prefix, hashAPIKey(token), expiresAt, createdBy, now).
		Scan(&k.ID, &k.TenantID, &k.MemberID, &k.Name, &k.Prefix, &k.ExpiresAt, &k.RevokedAt, &k.LastUsedAt, &k.CreatedBy, &k.CreatedAt)
	if err != nil {
		return "", nil, err
	}
	return token, &k, nil
}

// ResolveAPIKey finds the live key behind a presented token and stamps its last use. Revoked and
// expired keys fail with ErrAPIKeyInvalid.
func ResolveAPIKey(ctx context.Context, db DBTX, token string, now time.Time) (*APIKey, error) {
	if !IsAPIKey(token) {
		return nil, ErrAPIKeyInvalid
	}
	var k APIKey
	err := db.QueryRow(ctx, `UPDATE metadata.api_keys SET last_used_at = $2
		WHERE token_hash = $1 AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > $2)
		RETURNING id, tenant_id, member_id, name, prefix, expires_at, revoked_at, last_used_at, created_by, created_at`,
		hashAPIKey(token), now).
		Scan(&k.ID, &k.TenantID, &k.MemberID, &k.Name, &k.Prefix, &k.ExpiresAt, &k.RevokedAt, &k.LastUsedAt, &k.CreatedBy, &k.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrAPIKeyInvalid
	}
	return &k, err
}

// ListAPIKeys returns a member's keys, newest first, revoked ones included.
func ListAPIKeys(ctx context.Context, db DBTX, tenantID, memberID uuid.UUID) ([]APIKey, error) {
	rows, err := db.Query(ctx, `SELECT id, tenant_id, member_id, name, prefix, expires_at, revoked_at, last_used_at, created_by, created_at
		FROM metadata.api_keys WHERE tenant_id = $1 AND member_id = $2 ORDER BY created_at DESC`, tenantID, memberID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []APIKey{}
	for rows.Next() {
		var k APIKey
		if err := rows.Scan(&k.ID, &k.TenantID, &k.MemberID, &k.Name, &k.Prefix, &k.ExpiresAt, &k.RevokedAt, &k.LastUsedAt, &k.CreatedBy, &k.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// RevokeAPIKey revokes one key of the tenant (idempotent for an already revoked one).
func RevokeAPIKey(ctx context.Context, db DBTX, tenantID, keyID uuid.UUID, now time.Time) error {
	tag, err := db.Exec(ctx, `UPDATE metadata.api_keys SET revoked_at = coalesce(revoked_at, $3)
		WHERE id = $1 AND tenant_id = $2`, keyID, tenantID, now)
	return expectOne(tag, err)
}

// ListServiceAccounts returns the tenant's service accounts, ordered by name.
func ListServiceAccounts(ctx context.Context, db DBTX, tenantID uuid.UUID) ([]Member, error) {
	rows, err := db.Query(ctx, `SELECT `+memberColumns+` FROM metadata.users u
		JOIN metadata.identities i ON i.id = u.identity_id
		WHERE u.tenant_id = $1 AND u.deleted_at IS NULL AND u.auth_provider = $2
		ORDER BY lower(coalesce(u.display_name, '')), u.created_at`, tenantID, ServiceAccountProvider)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Member{}
	for rows.Next() {
		m, err := scanMember(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

// KeyTenant returns the member an API key belongs to, scoped to the tenant (ErrNotFound if the key
// is another tenant's).
func KeyTenant(ctx context.Context, db DBTX, tenantID, keyID uuid.UUID) (uuid.UUID, error) {
	var member uuid.UUID
	err := db.QueryRow(ctx, `SELECT member_id FROM metadata.api_keys WHERE id = $1 AND tenant_id = $2`, keyID, tenantID).Scan(&member)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrNotFound
	}
	return member, err
}
