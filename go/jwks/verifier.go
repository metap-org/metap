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

	"github.com/metap-org/metap-go/peripherals"
)

// TokenVerifierKind discriminates which trust root a TokenVerifier uses.
type TokenVerifierKind int

const (
	VerifierStatic TokenVerifierKind = iota
	VerifierJwks
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
}

// DecodeWithVerifier verifies token against whichever trust root verifier names — the one place
// this dispatch happens.
func DecodeWithVerifier(ctx context.Context, token string, verifier *TokenVerifier, leewayOverride *time.Duration) (*peripherals.AccessClaims, error) {
	leeway := verifier.Leeway
	if leewayOverride != nil {
		leeway = *leewayOverride
	}
	switch verifier.Kind {
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
