package jwks

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func downstreamSigner(t *testing.T) (*TokenSigner, *JwksKeyPair) {
	t.Helper()
	key, err := GenerateJwksKeyPair("kid-ds")
	require.NoError(t, err)
	return &TokenSigner{Kind: SignerJwks, Key: key}, key
}

func parse(t *testing.T, token string, key *JwksKeyPair, audience string) jwt.MapClaims {
	t.Helper()
	claims := jwt.MapClaims{}
	_, err := jwt.ParseWithClaims(token, claims, func(tok *jwt.Token) (any, error) {
		require.Equal(t, key.Kid, tok.Header["kid"])
		return key.PublicKey(), nil
	}, jwt.WithValidMethods([]string{"EdDSA"}), jwt.WithIssuer(JWTIssuer), jwt.WithAudience(audience), jwt.WithExpirationRequired())
	require.NoError(t, err)
	return claims
}

func TestADownstreamTokenCarriesItsAudienceTenantAndGrant(t *testing.T) {
	signer, key := downstreamSigner(t)
	tenant, user := uuid.New(), uuid.New()
	token, err := MintDownstream(signer, DownstreamToken{
		Audience: "waap-analytics", TenantID: tenant, UserID: user, TTL: time.Minute,
		Claims: map[string]any{"zoneAccess": "limited", "zones": []string{"z1", "z2"}},
	})
	require.NoError(t, err)

	claims := parse(t, token, key, "waap-analytics")
	require.Equal(t, tenant.String(), claims["tenantId"])
	require.Equal(t, user.String(), claims["sub"])
	require.Equal(t, "limited", claims["zoneAccess"])
	require.Equal(t, []any{"z1", "z2"}, claims["zones"])
	exp, _ := claims.GetExpirationTime()
	require.WithinDuration(t, time.Now().Add(time.Minute), exp.Time, 5*time.Second)
}

func TestADownstreamTokenIsNotAnOrdinaryPlatformToken(t *testing.T) {
	signer, key := downstreamSigner(t)
	token, err := MintDownstream(signer, DownstreamToken{Audience: "waap-analytics", TenantID: uuid.New(), UserID: uuid.New(), TTL: time.Minute})
	require.NoError(t, err)
	_, err = jwt.Parse(token, func(*jwt.Token) (any, error) { return key.PublicKey(), nil },
		jwt.WithValidMethods([]string{"EdDSA"}), jwt.WithAudience(JWTAudience))
	require.Error(t, err, "its audience is the downstream service's, so the platform's own verifier refuses it")
}

func TestADownstreamTokenCannotShadowAReservedClaim(t *testing.T) {
	signer, _ := downstreamSigner(t)
	for _, name := range []string{"iss", "aud", "sub", "exp", "iat", "tenantId"} {
		_, err := MintDownstream(signer, DownstreamToken{Audience: "x", TenantID: uuid.New(), UserID: uuid.New(), TTL: time.Minute,
			Claims: map[string]any{name: "forged"}})
		require.ErrorIs(t, err, ErrDownstream, name)
	}
}

func TestADownstreamTokenNeedsWhatItsMeaningRests(t *testing.T) {
	signer, _ := downstreamSigner(t)
	base := DownstreamToken{Audience: "x", TenantID: uuid.New(), UserID: uuid.New(), TTL: time.Minute}
	for name, mutate := range map[string]func(*DownstreamToken){
		"no audience": func(d *DownstreamToken) { d.Audience = "" },
		"no tenant":   func(d *DownstreamToken) { d.TenantID = uuid.Nil },
		"no ttl":      func(d *DownstreamToken) { d.TTL = 0 },
	} {
		d := base
		mutate(&d)
		_, err := MintDownstream(signer, d)
		require.ErrorIs(t, err, ErrDownstream, name)
	}
	_, err := MintDownstream(&TokenSigner{Kind: SignerStatic}, base)
	require.ErrorIs(t, err, ErrDownstream, "a static RSA signer has no kid to publish")
	_, err = MintDownstream(nil, base)
	require.ErrorIs(t, err, ErrDownstream)
}
