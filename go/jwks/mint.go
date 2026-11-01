// Mints a JWT signed by a JwksKeyPair — the JWKS-trust-root counterpart to
// peripherals.MintJWT (RS256, single static per-app key). Deliberately reuses that module's
// claim shape (sub/tenantId/functionId/exp/iss/aud, same JWTIssuer/JWTAudience constants) so a
// token minted here and one minted by MintJWT are semantically interchangeable claims-wise —
// only the signing key/algorithm and the presence of a kid header differ.
package jwks

import (
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/metap-org/metap-go/peripherals"
)

type jwksClaims struct {
	jwt.RegisteredClaims
	TenantID   string  `json:"tenantId"`
	FunctionID *string `json:"functionId,omitempty"`
	Scope      *string `json:"scope,omitempty"`
	ClientID   *string `json:"clientId,omitempty"`
}

// MintServiceOrUserJWT: a caller-on-behalf-of-user token (portal login, or forwarded end-user
// identity for an on-behalf-of-user gRPC call) or a machine-identity token for pure
// service-to-service calls — this function doesn't distinguish the two, since the claim shape is
// identical either way.
func MintServiceOrUserJWT(key *JwksKeyPair, tenantID, userID uuid.UUID, functionID *string, ttlSeconds uint64) (string, error) {
	return mintJwksClaims(key, tenantID, userID, functionID, ttlSeconds, nil, nil)
}

// MintScopedServiceOrUserJWT is the JWKS-trust-root counterpart to
// peripherals.MintOAuthAccessToken.
func MintScopedServiceOrUserJWT(key *JwksKeyPair, tenantID, userID uuid.UUID, ttlSeconds uint64, scope, clientID string) (string, error) {
	return mintJwksClaims(key, tenantID, userID, nil, ttlSeconds, &scope, &clientID)
}

func mintJwksClaims(key *JwksKeyPair, tenantID, userID uuid.UUID, functionID *string, ttlSeconds uint64, scope, clientID *string) (string, error) {
	now := time.Now().UTC()
	claims := jwksClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID.String(),
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Duration(ttlSeconds) * time.Second)),
			Issuer:    peripherals.JWTIssuer,
			Audience:  jwt.ClaimStrings{peripherals.JWTAudience},
		},
		TenantID:   tenantID.String(),
		FunctionID: functionID,
		Scope:      scope,
		ClientID:   clientID,
	}
	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims)
	token.Header["kid"] = key.Kid
	return token.SignedString(key.privateKey)
}
