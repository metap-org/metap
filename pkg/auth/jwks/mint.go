// Mints a JWT signed by a JwksKeyPair — the JWKS-trust-root counterpart to
// MintRSAJWT (RS256, single static per-app key). Deliberately reuses that module's
// claim shape (sub/tenantId/functionId/exp/iss/aud, same JWTIssuer/JWTAudience constants) so a
// token minted here and one minted by MintJWT are semantically interchangeable claims-wise —
// only the signing key/algorithm and the presence of a kid header differ.
package jwks

import (
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

type jwksClaims struct {
	jwt.RegisteredClaims
	TenantID   string  `json:"tenantId"`
	FunctionID *string `json:"functionId,omitempty"`
	Scope      *string `json:"scope,omitempty"`
	ClientID   *string `json:"clientId,omitempty"`
	SessionID  *string `json:"sid,omitempty"`
}

// MintServiceOrUserJWT: a caller-on-behalf-of-user token (portal login, or forwarded end-user
// identity for a call to another service on the user's behalf) or a machine-identity token for pure
// service-to-service calls — this function doesn't distinguish the two, since the claim shape is
// identical either way.
func MintServiceOrUserJWT(key *JwksKeyPair, tenantID, userID uuid.UUID, functionID *string, ttlSeconds uint64) (string, error) {
	return mintJwksClaims(key, tenantID, userID, functionID, ttlSeconds, nil, nil, nil)
}

// MintSessionJWT is MintServiceOrUserJWT for a login session: the token carries "sid".
func MintSessionJWT(key *JwksKeyPair, tenantID, userID uuid.UUID, ttlSeconds uint64, sessionID string) (string, error) {
	return mintJwksClaims(key, tenantID, userID, nil, ttlSeconds, nil, nil, &sessionID)
}

// MintScopedServiceOrUserJWT is the JWKS-trust-root counterpart to
// peripherals.MintOAuthAccessToken.
func MintScopedServiceOrUserJWT(key *JwksKeyPair, tenantID, userID uuid.UUID, ttlSeconds uint64, scope, clientID string) (string, error) {
	return mintJwksClaims(key, tenantID, userID, nil, ttlSeconds, &scope, &clientID, nil)
}

func mintJwksClaims(key *JwksKeyPair, tenantID, userID uuid.UUID, functionID *string, ttlSeconds uint64, scope, clientID, sessionID *string) (string, error) {
	now := time.Now().UTC()
	claims := jwksClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID.String(),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Duration(ttlSeconds) * time.Second)),
			Issuer:    JWTIssuer,
			Audience:  jwt.ClaimStrings{JWTAudience},
		},
		TenantID:   tenantID.String(),
		FunctionID: functionID,
		Scope:      scope,
		ClientID:   clientID,
		SessionID:  sessionID,
	}
	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims)
	token.Header["kid"] = key.Kid
	return token.SignedString(key.privateKey)
}
