// The mint-side counterpart to TokenVerifier — the same "which trust root" choice, made explicit
// for a login handler (not yet ported) instead of every minting call site hardcoding
// peripherals.MintJWT.
package jwks

import (
	"github.com/google/uuid"
	"github.com/metap-org/metap-go/peripherals"
)

type TokenSignerKind int

const (
	SignerStatic TokenSignerKind = iota
	SignerJwks
)

type TokenSigner struct {
	Kind TokenSignerKind

	// Static: raw PEM, matching peripherals.MintJWT's own signature.
	PrivateKeyPEM string

	// Jwks
	Key *JwksKeyPair
}

// MintWithSigner mints a token via whichever trust root signer names — see DecodeWithVerifier
// for the read-side equivalent this mirrors.
func MintWithSigner(signer *TokenSigner, tenantID, userID uuid.UUID, functionID *string, ttlSeconds uint64) (string, error) {
	switch signer.Kind {
	case SignerStatic:
		return peripherals.MintJWT(signer.PrivateKeyPEM, tenantID, userID, ttlSeconds)
	default:
		return MintServiceOrUserJWT(signer.Key, tenantID, userID, functionID, ttlSeconds)
	}
}

// MintOAuthTokenWithSigner is the OAuth2-authorization-server counterpart to MintWithSigner.
func MintOAuthTokenWithSigner(signer *TokenSigner, tenantID, userID uuid.UUID, ttlSeconds uint64, scope, clientID string) (string, error) {
	switch signer.Kind {
	case SignerStatic:
		return peripherals.MintOAuthAccessToken(signer.PrivateKeyPEM, tenantID, userID, ttlSeconds, scope, clientID)
	default:
		return MintScopedServiceOrUserJWT(signer.Key, tenantID, userID, ttlSeconds, scope, clientID)
	}
}
