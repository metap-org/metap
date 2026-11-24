package jwks

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// DownstreamToken describes a short-lived token the issuer signs for ANOTHER service of the same
// product (an analytics API, a search service), carrying a grant that service enforces itself.
//
// It is not a login or API token: its audience is the downstream service's own, so the ordinary
// platform verifier refuses it and the downstream service refuses an ordinary token. What the
// downstream may do is whatever Claims says, decided by the caller when it was minted (from the
// caller's permissions at that moment), and bounded by TTL.
type DownstreamToken struct {
	// Audience names the service the token is for.
	Audience string
	TenantID uuid.UUID
	UserID   uuid.UUID
	TTL      time.Duration
	// Claims are the grant, added next to the registered ones. A name that would shadow a registered
	// claim or tenantId is refused.
	Claims map[string]any
}

// ErrDownstream is returned for a token that cannot be minted as described.
var ErrDownstream = errors.New("jwks: invalid downstream token")

var reservedClaims = map[string]bool{"iss": true, "aud": true, "sub": true, "exp": true, "iat": true, "nbf": true, "jti": true, "tenantId": true}

// MintDownstream signs t with the issuer's current key. Only the key-based signers (JWKS and the
// shared key store) can: a static RSA key has no kid for a downstream service to look up.
func MintDownstream(signer *TokenSigner, t DownstreamToken) (string, error) {
	if signer == nil || signer.Kind == SignerStatic {
		return "", fmt.Errorf("%w: the signer has no JWKS key", ErrDownstream)
	}
	key := signer.jwksKey()
	if key == nil {
		return "", fmt.Errorf("%w: the signer has no signing key", ErrDownstream)
	}
	if t.Audience == "" || t.TTL <= 0 || t.TenantID == uuid.Nil {
		return "", fmt.Errorf("%w: an audience, a tenant and a positive ttl are required", ErrDownstream)
	}
	now := time.Now().UTC()
	claims := jwt.MapClaims{
		"iss":      JWTIssuer,
		"aud":      jwt.ClaimStrings{t.Audience},
		"sub":      t.UserID.String(),
		"iat":      jwt.NewNumericDate(now),
		"exp":      jwt.NewNumericDate(now.Add(t.TTL)),
		"tenantId": t.TenantID.String(),
	}
	for name, value := range t.Claims {
		if reservedClaims[name] {
			return "", fmt.Errorf("%w: claim %q is reserved", ErrDownstream, name)
		}
		claims[name] = value
	}
	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims)
	token.Header["kid"] = key.Kid
	return token.SignedString(key.privateKey)
}
