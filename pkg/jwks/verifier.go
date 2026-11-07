// The verify-side dispatch every transport that accepts this platform's bearer/session tokens
// needs: a token is either signed by a binary's own static per-app RS256 keypair
// (peripherals.DecodeAccessToken) or by the JWKS multi-service trust root (JwksClient.Decode).
// Both produce the same AccessClaims shape, so a caller downstream of either can't tell which
// one verified a given request's token.
package jwks

import (
	"context"
	"crypto/rsa"
	"fmt"
	"time"

	"github.com/metap-org/metap/pkg/peripherals"
)

// TokenVerifierKind discriminates which trust root a TokenVerifier uses.
type TokenVerifierKind int

const (
	VerifierStatic TokenVerifierKind = iota
	VerifierJwks
	// VerifierAuto: a token whose header names a kid is verified against Keys (EdDSA/JWKS);
	// one without a kid falls back to PublicKey (static RS256) when that is set, and is
	// rejected when it isn't. Lets services move from the shared RSA keypair to a single JWKS
	// issuer without invalidating sessions already issued: keep PublicKey set for one token
	// lifetime after the cutover, then drop it.
	VerifierAuto
)

// TokenVerifier is either verification path an issuing deployment might configure — exactly one
// of the two, never both, since a token is signed by one trust root or the other.
type TokenVerifier struct {
	Kind TokenVerifierKind

	// Static
	PublicKey *rsa.PublicKey
	Leeway    time.Duration

	// Jwks
	Client *JwksClient
	// Keys: VerifierAuto's EdDSA key source — the issuer's own *SharedKeyStore, or a
	// *JwksClient everywhere else.
	Keys KeySource
}

// DecodeWithVerifier verifies token against whichever trust root verifier names — the one place
// this dispatch happens.
func DecodeWithVerifier(ctx context.Context, token string, verifier *TokenVerifier, leewayOverride *time.Duration) (*peripherals.AccessClaims, error) {
	leeway := verifier.Leeway
	if leewayOverride != nil {
		leeway = *leewayOverride
	}
	switch verifier.Kind {
	case VerifierAuto:
		if HasKid(token) {
			if verifier.Keys == nil {
				return nil, fmt.Errorf("invalid or expired token: no JWKS key source configured")
			}
			claims, err := DecodeEdDSA(ctx, token, verifier.Keys, leeway)
			if err != nil {
				return nil, fmt.Errorf("invalid or expired token: %w", err)
			}
			return claims, nil
		}
		if verifier.PublicKey == nil {
			return nil, fmt.Errorf("invalid or expired token: token has no kid and static RSA verification is disabled")
		}
		claims, err := peripherals.DecodeAccessToken(token, verifier.PublicKey, leeway)
		if err != nil {
			return nil, fmt.Errorf("invalid or expired token: %w", err)
		}
		return claims, nil
	case VerifierStatic:
		claims, err := peripherals.DecodeAccessToken(token, verifier.PublicKey, leeway)
		if err != nil {
			return nil, fmt.Errorf("invalid or expired token: %w", err)
		}
		return claims, nil
	default:
		claims, err := verifier.Client.Decode(ctx, token, leeway)
		if err != nil {
			return nil, fmt.Errorf("invalid or expired token: %w", err)
		}
		return claims, nil
	}
}
