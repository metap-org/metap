// Local username/password auth — the first thing in this package that stores a credential;
// everything else only ever verifies a bearer JWT someone else minted. Two responsibilities kept
// together because they're both "identity", not because they're logically inseparable:
//
//   - CreateUser/VerifyCredentials: thin wrappers over pkg/identity (identities + memberships).
//   - MintJWT: the only JWT-encoding implementation in this package — both a CLI mint-token
//     command and an HTTP login handler should call this instead of each
//     hand-rolling their own Claims struct, so the two paths can't drift on claim shape.
package peripherals

import (
	"context"
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/metap-org/metap/pkg/identity"
)

// JWTIssuer/JWTAudience: iss/aud claim values every token this package mints carries, and the only
// values a decode-side Validation should accept — the HTTP auth layer should import these same
// constants rather than duplicating the literals, so mint and verify can't drift.
const (
	JWTIssuer   = "metap"
	JWTAudience = "metap-api"
)

type AuthUser struct {
	ID       uuid.UUID
	TenantID uuid.UUID
	Email    string
}

func authUser(m *identity.Member) *AuthUser {
	return &AuthUser{ID: m.ID, TenantID: m.TenantID, Email: m.Email}
}

// CreateUser adds email to tenantID as a local-login member, creating the person's identity
// (with password) when they have none yet — see identity.AddMember. db must reach the shared
// database (pool, or a transaction whose search_path includes metadata).
func CreateUser(ctx context.Context, db DBTX, tenantID uuid.UUID, email, password string) (*AuthUser, error) {
	m, _, err := identity.AddMember(ctx, db, identity.NewMember{TenantID: tenantID, Email: email, Password: password})
	if err != nil {
		return nil, err
	}
	return authUser(m), nil
}

// FindUserByID looks up one member by id, scoped to the tenant so an id from another tenant can
// never resolve. Returns (nil, nil) when not found.
func FindUserByID(ctx context.Context, db DBTX, tenantID, userID uuid.UUID) (*AuthUser, error) {
	m, err := identity.GetMember(ctx, db, tenantID, userID)
	if errors.Is(err, identity.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return authUser(m), nil
}

// ListTenantUsers returns every active member of the tenant, id+email only — the "pick a user"
// primitive an app-level assignee/reporter picker needs. Any authenticated user may call the route
// this backs — picking who to assign something to isn't an admin action.
func ListTenantUsers(ctx context.Context, db DBTX, tenantID uuid.UUID) ([]*AuthUser, error) {
	out := make([]*AuthUser, 0)
	for offset := 0; ; offset += identity.MaxListLimit {
		page, total, err := identity.ListMembers(ctx, db, tenantID, identity.ListQuery{
			Status: identity.StatusActive, Offset: offset, Limit: identity.MaxListLimit,
		})
		if err != nil {
			return nil, err
		}
		for i := range page {
			out = append(out, authUser(&page[i]))
		}
		if offset+len(page) >= total || len(page) == 0 {
			return out, nil
		}
	}
}

// VerifyCredentials checks email + password and returns the member to sign in as: the member of
// tenantID when it is set, else the person's only (or last used) tenant. (nil, nil) for wrong
// credentials, no usable membership, or a person in several tenants with none chosen — the
// caller cannot tell these apart, by design.
func VerifyCredentials(ctx context.Context, db DBTX, email, password string, tenantID *uuid.UUID) (*AuthUser, error) {
	ident, err := identity.Authenticate(ctx, db, email, password, time.Now())
	if err != nil {
		if errors.Is(err, identity.ErrInvalidCredentials) || errors.Is(err, identity.ErrLocked) || errors.Is(err, identity.ErrDisabled) {
			return nil, nil
		}
		return nil, err
	}
	// A password alone is not enough for someone with a second factor (roadmap 115 M3): this path
	// (HTTP Basic, provider logins built on it) has no second step, so it refuses rather than
	// bypassing MFA. Such a person signs in through /auth/login, and a script uses an API key.
	if on, err := identity.MFAEnabled(ctx, db, ident.ID); err != nil {
		return nil, err
	} else if on {
		return nil, nil
	}
	memberships, err := identity.Memberships(ctx, db, ident.ID)
	if err != nil {
		return nil, err
	}
	m, ok := identity.PickMembership(memberships, tenantID, ident.LastTenantID)
	if !ok {
		return nil, nil
	}
	return &AuthUser{ID: m.MemberID, TenantID: m.TenantID, Email: ident.Email}, nil
}

type claims struct {
	jwt.RegisteredClaims
	TenantID string `json:"tenantId"`
	Scope    string `json:"scope,omitempty"`
	ClientID string `json:"clientId,omitempty"`
	// SessionID ("sid") ties a login session's token to its row in metadata.sessions, so one
	// session can be listed and revoked on its own (roadmap 115 M4).
	SessionID string `json:"sid,omitempty"`
}

// MintJWT mints an RS256 JWT with the claim shape a decode-side AuthContext extractor expects
// (sub, tenantId, exp — no functionId, which that extractor already treats as optional).
// privateKeyPEM is the raw PEM text, not a pre-parsed key, so callers (both of which mint
// infrequently — an interactive CLI command and a login request) don't need to hold a parsed key
// in state just for this.
func MintJWT(privateKeyPEM string, tenantID, userID uuid.UUID, ttlSeconds uint64) (string, error) {
	return mintClaims(privateKeyPEM, tenantID, userID, ttlSeconds, "", "", "")
}

// MintSessionJWT is MintJWT for a login session: the token carries the session id as "sid".
func MintSessionJWT(privateKeyPEM string, tenantID, userID uuid.UUID, ttlSeconds uint64, sessionID string) (string, error) {
	return mintClaims(privateKeyPEM, tenantID, userID, ttlSeconds, "", "", sessionID)
}

// MintOAuthAccessToken is the OAuth2-authorization-server counterpart to MintJWT — same trust
// root, same claim shape plus scope/clientId. A separate function rather than two new parameters
// on MintJWT itself — that signature is called from several places; adding parameters there
// would be a breaking change to all of them for a capability only the OAuth2 token endpoint
// needs.
func MintOAuthAccessToken(privateKeyPEM string, tenantID, userID uuid.UUID, ttlSeconds uint64, scope, clientID string) (string, error) {
	return mintClaims(privateKeyPEM, tenantID, userID, ttlSeconds, scope, clientID, "")
}

func mintClaims(privateKeyPEM string, tenantID, userID uuid.UUID, ttlSeconds uint64, scope, clientID, sessionID string) (string, error) {
	key, err := jwt.ParseRSAPrivateKeyFromPEM([]byte(privateKeyPEM))
	if err != nil {
		return "", err
	}
	now := time.Now().UTC()
	c := claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID.String(),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Duration(ttlSeconds) * time.Second)),
			Issuer:    JWTIssuer,
			Audience:  jwt.ClaimStrings{JWTAudience},
			ID:        uuid.NewString(),
		},
		TenantID:  tenantID.String(),
		Scope:     scope,
		ClientID:  clientID,
		SessionID: sessionID,
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
	// SessionID is the "sid" claim of a login-session token; nil for every other kind of token.
	SessionID *string
	// IssuedAt lets the session check reject a token issued before its user's sessions were
	// revoked (password change, disable). nil for a token minted before iat was added.
	IssuedAt *time.Time
}

type accessClaimsWire struct {
	jwt.RegisteredClaims
	TenantID   string  `json:"tenantId"`
	FunctionID *string `json:"functionId,omitempty"`
	Scope      *string `json:"scope,omitempty"`
	ClientID   *string `json:"clientId,omitempty"`
	SessionID  *string `json:"sid,omitempty"`
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
		SessionID:  wire.SessionID,
		IssuedAt:   IssuedAt(wire.IssuedAt),
	}, nil
}

// IssuedAt is nil for a token minted before tokens carried iat.
func IssuedAt(d *jwt.NumericDate) *time.Time {
	if d == nil {
		return nil
	}
	t := d.Time
	return &t
}
