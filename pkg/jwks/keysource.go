package jwks

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/metap-org/metap/pkg/peripherals"
)

// KeySource resolves a token's kid to the Ed25519 public key that must have signed it. The
// issuer itself uses its in-memory *SharedKeyStore (no HTTP round trip to itself); every other
// service uses a *JwksClient pointed at the issuer's /.well-known/jwks.json.
type KeySource interface {
	PublicKeyFor(ctx context.Context, kid string) (ed25519.PublicKey, error)
}

var (
	_ KeySource = (*JwksClient)(nil)
	_ KeySource = (*SharedKeyStore)(nil)
)

// HasKid reports whether token's (unverified) header names a key id — how a mixed verifier tells
// an EdDSA/JWKS token from a legacy static-RSA one (which never carries a kid).
func HasKid(token string) bool {
	unverified, _, err := jwt.NewParser().ParseUnverified(token, jwt.MapClaims{})
	if err != nil {
		return false
	}
	kid, ok := unverified.Header["kid"].(string)
	return ok && kid != ""
}

// DecodeEdDSA verifies token's signature with the key its kid names in src, plus the standard
// audience/issuer/expiry checks, and returns its claims.
func DecodeEdDSA(ctx context.Context, tokenString string, src KeySource, leeway time.Duration) (*peripherals.AccessClaims, error) {
	unverified, _, err := jwt.NewParser().ParseUnverified(tokenString, jwt.MapClaims{})
	if err != nil {
		return nil, err
	}
	kid, _ := unverified.Header["kid"].(string)
	if kid == "" {
		return nil, fmt.Errorf("token has no kid — cannot select a JWKS verification key")
	}
	key, err := src.PublicKeyFor(ctx, kid)
	if err != nil {
		return nil, err
	}
	var wire jwksClaims
	_, err = jwt.ParseWithClaims(tokenString, &wire, func(t *jwt.Token) (any, error) { return key, nil },
		jwt.WithValidMethods([]string{"EdDSA"}),
		jwt.WithAudience(peripherals.JWTAudience),
		jwt.WithIssuer(peripherals.JWTIssuer),
		jwt.WithLeeway(leeway),
		jwt.WithExpirationRequired(),
	)
	if err != nil {
		return nil, err
	}
	var jti *string
	if wire.ID != "" {
		jti = &wire.ID
	}
	return &peripherals.AccessClaims{
		Subject:    wire.Subject,
		TenantID:   wire.TenantID,
		FunctionID: wire.FunctionID,
		JTI:        jti,
		Scope:      wire.Scope,
		ClientID:   wire.ClientID,
	}, nil
}
