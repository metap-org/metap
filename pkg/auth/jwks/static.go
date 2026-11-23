package jwks

import (
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// JWTIssuer/JWTAudience: iss/aud claim values every token this package mints carries, and the only
// values a decode-side Validation should accept — the HTTP auth layer should import these same
// constants rather than duplicating the literals, so mint and verify can't drift.
const (
	JWTIssuer   = "metap"
	JWTAudience = "metap-api"
)

type claims struct {
	jwt.RegisteredClaims
	TenantID string `json:"tenantId"`
	Scope    string `json:"scope,omitempty"`
	ClientID string `json:"clientId,omitempty"`
	// SessionID ("sid") ties a login session's token to its row in metadata.sessions, so one
	// session can be listed and revoked on its own (roadmap 115 M4).
	SessionID string `json:"sid,omitempty"`
}

// MintRSAJWT mints an RS256 JWT with the claim shape a decode-side AuthContext extractor expects
// (sub, tenantId, exp — no functionId, which that extractor already treats as optional).
// privateKeyPEM is the raw PEM text, not a pre-parsed key, so callers (both of which mint
// infrequently — an interactive CLI command and a login request) don't need to hold a parsed key
// in state just for this.
func MintRSAJWT(privateKeyPEM string, tenantID, userID uuid.UUID, ttlSeconds uint64) (string, error) {
	return mintClaims(privateKeyPEM, tenantID, userID, ttlSeconds, "", "", "")
}

// MintRSASessionJWT is MintJWT for a login session: the token carries the session id as "sid".
func MintRSASessionJWT(privateKeyPEM string, tenantID, userID uuid.UUID, ttlSeconds uint64, sessionID string) (string, error) {
	return mintClaims(privateKeyPEM, tenantID, userID, ttlSeconds, "", "", sessionID)
}

// MintRSAOAuthAccessToken is the OAuth2-authorization-server counterpart to MintJWT — same trust
// root, same claim shape plus scope/clientId. A separate function rather than two new parameters
// on MintJWT itself — that signature is called from several places; adding parameters there
// would be a breaking change to all of them for a capability only the OAuth2 token endpoint
// needs.
func MintRSAOAuthAccessToken(privateKeyPEM string, tenantID, userID uuid.UUID, ttlSeconds uint64, scope, clientID string) (string, error) {
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
