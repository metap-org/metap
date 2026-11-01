// Local username/password auth — the first thing in this package that stores a credential;
// everything else only ever verifies a bearer JWT someone else minted. Two responsibilities kept
// together because they're both "identity", not because they're logically inseparable:
//
//   - CreateUser/VerifyCredentials: the users table (argon2id password hashing).
//   - MintJWT: the only JWT-encoding implementation in this package — both a CLI mint-token
//     command and an HTTP login handler (not yet ported) should call this instead of each
//     hand-rolling their own Claims struct, so the two paths can't drift on claim shape.
package peripherals

import (
	"context"
	"sync"
	"time"

	"github.com/alexedwards/argon2id"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// JWTIssuer/JWTAudience: iss/aud claim values every token this package mints carries, and the
// only values a decode-side Validation should accept — the HTTP auth layer (not yet ported)
// should import these same constants rather than duplicating the literals, so mint and verify
// can't drift.
const (
	JWTIssuer   = "metap"
	JWTAudience = "metap-api"
)

// argon2idParams mirrors the Rust argon2 crate's Params::DEFAULT (v0.5.3): m_cost=19456 KiB,
// t_cost=2, p_cost=1, 32-byte output — chosen so a password hash is encoded in the same
// PHC string format ($argon2id$v=19$m=...,t=...,p=...$salt$hash) either implementation produces,
// should this service and the Rust original ever need to read each other's users table.
var argon2idParams = &argon2id.Params{
	Memory:      19456,
	Iterations:  2,
	Parallelism: 1,
	SaltLength:  16,
	KeyLength:   32,
}

type AuthUser struct {
	ID       uuid.UUID
	TenantID uuid.UUID
	Email    string
}

func hashPassword(password string) (string, error) {
	return argon2id.CreateHash(password, argon2idParams)
}

func verifyPassword(password, hash string) (bool, error) {
	match, err := argon2id.ComparePasswordAndHash(password, hash)
	if err != nil {
		return false, err
	}
	return match, nil
}

// dummyHash is a precomputed hash VerifyCredentials checks against when no user matches — so
// "email doesn't exist" and "email exists, wrong password" both pay the same (deliberately slow)
// argon2 verify cost instead of the former returning near-instantly, which would otherwise let a
// caller enumerate registered emails by timing.
var dummyHashOnce = sync.OnceValues(func() (string, error) {
	return hashPassword("dummy-password-for-timing-safety")
})

func CreateUser(ctx context.Context, db DBTX, tenantID uuid.UUID, email, password string) (*AuthUser, error) {
	passwordHash, err := hashPassword(password)
	if err != nil {
		return nil, err
	}
	var u AuthUser
	err = db.QueryRow(ctx,
		"INSERT INTO users (tenant_id, email, password_hash) VALUES ($1, $2, $3) RETURNING id, tenant_id, email",
		tenantID, email, passwordHash,
	).Scan(&u.ID, &u.TenantID, &u.Email)
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// FindUserByID looks up one user by id, scoped to the tenant so an id from another tenant can
// never resolve. Returns (nil, nil) when not found.
func FindUserByID(ctx context.Context, db DBTX, tenantID, userID uuid.UUID) (*AuthUser, error) {
	var u AuthUser
	err := db.QueryRow(ctx, "SELECT id, tenant_id, email FROM users WHERE id = $1 AND tenant_id = $2",
		userID, tenantID).Scan(&u.ID, &u.TenantID, &u.Email)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &u, nil
}

// ListTenantUsers returns every user in the tenant, id+email only — the "pick a user" primitive
// an app-level assignee/reporter picker needs. Any authenticated user may call the route this
// backs — picking who to assign something to isn't an admin action, unlike granting a role
// (role_assignment.ListUsers).
func ListTenantUsers(ctx context.Context, db DBTX, tenantID uuid.UUID) ([]*AuthUser, error) {
	rows, err := db.Query(ctx, "SELECT id, tenant_id, email FROM users WHERE tenant_id = $1 ORDER BY email", tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// make(..., 0), not var out []*AuthUser — a nil Go slice marshals to JSON null, not [],
	// diverging from the Rust Vec<T> wire shape GET /users' caller expects on an empty tenant.
	out := make([]*AuthUser, 0)
	for rows.Next() {
		var u AuthUser
		if err := rows.Scan(&u.ID, &u.TenantID, &u.Email); err != nil {
			return nil, err
		}
		out = append(out, &u)
	}
	return out, rows.Err()
}

// VerifyCredentials returns (nil, nil) for either "no user with this email" or "wrong password"
// — deliberately not distinguished, so a caller can't use this to enumerate registered emails.
//
// Looks up email globally, not (tenant_id, email) — deliberate, matching the Rust original's own
// audit finding: a login request has no tenant picker, a user types only email+password, and
// this lookup is what resolves which tenant they belong to. One email means one account
// platform-wide, by design, until a real tenant-picker UI exists.
func VerifyCredentials(ctx context.Context, db DBTX, email, password string) (*AuthUser, error) {
	var u AuthUser
	var storedHash string
	err := db.QueryRow(ctx, "SELECT id, tenant_id, email, password_hash FROM users WHERE email = $1", email).
		Scan(&u.ID, &u.TenantID, &u.Email, &storedHash)
	if err != nil {
		if err == pgx.ErrNoRows {
			dummy, dErr := dummyHashOnce()
			if dErr != nil {
				return nil, dErr
			}
			_, _ = verifyPassword(password, dummy)
			return nil, nil
		}
		return nil, err
	}

	ok, err := verifyPassword(password, storedHash)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	return &u, nil
}

// claims mirrors the Rust Claims struct exactly — the HTTP auth layer's decode side (not yet
// ported) must keep matching this shape.
type claims struct {
	jwt.RegisteredClaims
	TenantID string `json:"tenantId"`
	Scope    string `json:"scope,omitempty"`
	ClientID string `json:"clientId,omitempty"`
}

// MintJWT mints an RS256 JWT with the claim shape a decode-side AuthContext extractor expects
// (sub, tenantId, exp — no functionId, which that extractor already treats as optional).
// privateKeyPEM is the raw PEM text, not a pre-parsed key, so callers (both of which mint
// infrequently — an interactive CLI command and a login request) don't need to hold a parsed key
// in state just for this.
func MintJWT(privateKeyPEM string, tenantID, userID uuid.UUID, ttlSeconds uint64) (string, error) {
	return mintClaims(privateKeyPEM, tenantID, userID, ttlSeconds, "", "")
}

// MintOAuthAccessToken is the OAuth2-authorization-server counterpart to MintJWT — same trust
// root, same claim shape plus scope/clientId. A separate function rather than two new parameters
// on MintJWT itself — that signature is called from several places; adding parameters there
// would be a breaking change to all of them for a capability only the OAuth2 token endpoint
// needs.
func MintOAuthAccessToken(privateKeyPEM string, tenantID, userID uuid.UUID, ttlSeconds uint64, scope, clientID string) (string, error) {
	return mintClaims(privateKeyPEM, tenantID, userID, ttlSeconds, scope, clientID)
}

func mintClaims(privateKeyPEM string, tenantID, userID uuid.UUID, ttlSeconds uint64, scope, clientID string) (string, error) {
	key, err := jwt.ParseRSAPrivateKeyFromPEM([]byte(privateKeyPEM))
	if err != nil {
		return "", err
	}
	now := time.Now().UTC()
	c := claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID.String(),
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Duration(ttlSeconds) * time.Second)),
			Issuer:    JWTIssuer,
			Audience:  jwt.ClaimStrings{JWTAudience},
			ID:        uuid.NewString(),
		},
		TenantID: tenantID.String(),
		Scope:    scope,
		ClientID: clientID,
	}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, c)
	return token.SignedString(key)
}

// AccessClaims is the decode-side counterpart to MintJWT's claim shape. FunctionID/JTI/Scope/
// ClientID are all optional since a token minted before one of them existed must still decode —
// this platform has no forced-reauth step.
type AccessClaims struct {
	Subject    string
	TenantID   string
	FunctionID *string
	JTI        *string
	Scope      *string
	ClientID   *string
}

type accessClaimsWire struct {
	jwt.RegisteredClaims
	TenantID   string  `json:"tenantId"`
	FunctionID *string `json:"functionId,omitempty"`
	Scope      *string `json:"scope,omitempty"`
	ClientID   *string `json:"clientId,omitempty"`
}

// DecodeAccessToken verifies a Bearer access token minted by MintJWT: RS256, audience/issuer
// pinned to JWTAudience/JWTIssuer, leeway seconds of clock-skew tolerance. Pure — no DB, no role
// lookup, no RequestContext construction (that needs a tenant-scoped Router, which this package
// deliberately doesn't depend on to avoid a cycle with the control-plane package). The single
// place JWT verification logic lives, so every transport that accepts this platform's bearer
// tokens calls this instead of re-implementing validation setup.
func DecodeAccessToken(tokenString string, publicKey any, leeway time.Duration) (*AccessClaims, error) {
	var wire accessClaimsWire
	_, err := jwt.ParseWithClaims(tokenString, &wire, func(t *jwt.Token) (any, error) {
		return publicKey, nil
	},
		jwt.WithValidMethods([]string{"RS256"}),
		jwt.WithAudience(JWTAudience),
		jwt.WithIssuer(JWTIssuer),
		jwt.WithLeeway(leeway),
	)
	if err != nil {
		return nil, err
	}

	var jti *string
	if wire.ID != "" {
		jti = &wire.ID
	}
	return &AccessClaims{
		Subject:    wire.Subject,
		TenantID:   wire.TenantID,
		FunctionID: wire.FunctionID,
		JTI:        jti,
		Scope:      wire.Scope,
		ClientID:   wire.ClientID,
	}, nil
}
