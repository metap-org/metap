//go:build integration

package httpserver_test

import (
	"context"
	"crypto/rsa"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/jwks"
	"github.com/metap-org/metap/pkg/peripherals"
)

// Single-issuer JWKS over real HTTP: one AppState is the issuer (signs, publishes
// /.well-known/jwks.json), another only verifies against it — the topology ../waap runs
// (zones-service issues, scanning/alerting/graphql-gateway verify).
func TestSingleIssuerJWKSLoginVerifyAndCutover(t *testing.T) {
	ctx := context.Background()
	pool := httpTestPool(t)
	tenantID := uuid.New()
	_, err := peripherals.CreateUser(ctx, pool, tenantID, "jwks-user@test.local", "pass123")
	require.NoError(t, err)

	// Issuer.
	issuer, legacyPEM := newTestAppState(t, pool, httpTestEntity())
	legacyPub := issuer.JWTPublicKey
	key, err := jwks.GenerateJwksKeyPair("kid-1")
	require.NoError(t, err)
	keys := jwks.NewSharedKeyStore(jwks.NewJwksKeyStore(key))
	issuer.JWTPrivateKeyPEM = ""
	issuer.JwksKeys = keys
	issuer.TokenSigner = &jwks.TokenSigner{Kind: jwks.SignerStore, Store: keys}
	issuer.TokenVerifier = &jwks.TokenVerifier{Kind: jwks.VerifierAuto, Keys: keys, Leeway: time.Second}
	issuerBase := startTestServer(t, issuer, nil)

	login := doRequest(t, http.MethodPost, issuerBase+"/auth/login", "",
		map[string]any{"email": "jwks-user@test.local", "password": "pass123", "tenantId": tenantID.String()})
	require.Equal(t, http.StatusOK, login.StatusCode)
	token := decodeJSON(t, login)["data"].(map[string]any)["token"].(string)
	require.True(t, jwks.HasKid(token), "the issuer signs EdDSA with a kid")

	jwksDoc := decodeJSON(t, doRequest(t, http.MethodGet, issuerBase+"/.well-known/jwks.json", "", nil))
	require.Len(t, jwksDoc["keys"], 1)
	discovery := decodeJSON(t, doRequest(t, http.MethodGet, issuerBase+"/.well-known/oauth-authorization-server", "", nil))
	assert.Equal(t, "/.well-known/jwks.json", discovery["jwks_uri"])

	me := doRequest(t, http.MethodGet, issuerBase+"/auth/me", token, nil)
	require.Equal(t, http.StatusOK, me.StatusCode, "the issuer verifies its own token from memory")
	me.Body.Close()

	// Verifier: no signing key at all, trusts the issuer's JWKS.
	verifier, _ := newTestAppState(t, pool, httpTestEntity())
	verifier.JWTPrivateKeyPEM = ""
	client := jwks.NewJwksClient(issuerBase+"/.well-known/jwks.json", time.Minute)
	verifier.TokenVerifier = &jwks.TokenVerifier{Kind: jwks.VerifierAuto, Keys: client, Leeway: time.Second}
	verifierBase := startTestServer(t, verifier, nil)

	me = doRequest(t, http.MethodGet, verifierBase+"/auth/me", token, nil)
	require.Equal(t, http.StatusOK, me.StatusCode, "a verifier accepts the issuer's token via JWKS")
	me.Body.Close()

	notIssuer := doRequest(t, http.MethodPost, verifierBase+"/auth/login", "",
		map[string]any{"email": "jwks-user@test.local", "password": "wrong", "tenantId": tenantID.String()})
	require.Equal(t, http.StatusNotImplemented, notIssuer.StatusCode, "checked before the password")
	assert.Equal(t, "not_token_issuer", decodeJSON(t, notIssuer)["error"].(map[string]any)["code"])
	discovery = decodeJSON(t, doRequest(t, http.MethodGet, verifierBase+"/.well-known/oauth-authorization-server", "", nil))
	assert.NotContains(t, discovery, "jwks_uri")

	// Cutover: a session issued under the old shared RSA key is rejected by a JWKS-only verifier,
	// accepted while the RSA public key is still configured as a fallback.
	legacy, err := peripherals.MintJWT(legacyPEM, tenantID, uuid.New(), 300)
	require.NoError(t, err)
	me = doRequest(t, http.MethodGet, verifierBase+"/auth/me", legacy, nil)
	require.Equal(t, http.StatusUnauthorized, me.StatusCode)
	me.Body.Close()

	verifier.TokenVerifier.PublicKey = legacyPub.(*rsa.PublicKey)
	me = doRequest(t, http.MethodGet, verifierBase+"/auth/me", legacy, nil)
	require.NotEqual(t, http.StatusUnauthorized, me.StatusCode, "legacy RS256 sessions verify during the cutover")
	me.Body.Close()

	// A token from a different issuer that happens to reuse the kid is rejected.
	rogue, _ := jwks.GenerateJwksKeyPair("kid-1")
	forged, err := jwks.MintServiceOrUserJWT(rogue, tenantID, uuid.New(), nil, 300)
	require.NoError(t, err)
	me = doRequest(t, http.MethodGet, verifierBase+"/auth/me", forged, nil)
	require.Equal(t, http.StatusUnauthorized, me.StatusCode)
	me.Body.Close()

}
