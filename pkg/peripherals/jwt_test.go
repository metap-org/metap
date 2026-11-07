package peripherals

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func generateTestRSAKeyPair(t *testing.T) (privatePEM string, publicKey *rsa.PublicKey) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	der := x509.MarshalPKCS1PrivateKey(key)
	block := &pem.Block{Type: "RSA PRIVATE KEY", Bytes: der}
	return string(pem.EncodeToMemory(block)), &key.PublicKey
}

func TestMintJWTAndDecodeAccessTokenRoundTrip(t *testing.T) {
	privatePEM, publicKey := generateTestRSAKeyPair(t)
	tenantID := uuid.New()
	userID := uuid.New()

	token, err := MintJWT(privatePEM, tenantID, userID, 3600)
	require.NoError(t, err)

	claims, err := DecodeAccessToken(token, publicKey, 0)
	require.NoError(t, err)
	require.Equal(t, userID.String(), claims.Subject, "expected sub=%s, got %s", userID, claims.Subject)
	require.Equal(t, tenantID.String(), claims.TenantID, "expected tenantId=%s, got %s", tenantID, claims.TenantID)
	require.NotNil(t, claims.JTI, "expected a non-empty jti")
	require.NotEqual(t, "", *claims.JTI, "expected a non-empty jti")
	require.Nil(t, claims.Scope, "expected no scope on a plain session token")
}

func TestMintOAuthAccessTokenCarriesScopeAndClientID(t *testing.T) {
	privatePEM, publicKey := generateTestRSAKeyPair(t)
	tenantID := uuid.New()
	userID := uuid.New()

	token, err := MintOAuthAccessToken(privatePEM, tenantID, userID, 3600, "read:zones", "mcl_abc123")
	require.NoError(t, err)

	claims, err := DecodeAccessToken(token, publicKey, 0)
	require.NoError(t, err)
	require.NotNil(t, claims.Scope, "expected scope=read:zones, got %v", claims.Scope)
	require.Equal(t, "read:zones", *claims.Scope, "expected scope=read:zones, got %v", claims.Scope)
	require.NotNil(t, claims.ClientID, "expected clientId=mcl_abc123, got %v", claims.ClientID)
	require.Equal(t, "mcl_abc123", *claims.ClientID, "expected clientId=mcl_abc123, got %v", claims.ClientID)
}

func TestDecodeAccessTokenRejectsATokenSignedByADifferentKey(t *testing.T) {
	_, publicKey := generateTestRSAKeyPair(t)

	// A token signed with a *different* key must fail verification outright.
	otherPrivatePEM, _ := generateTestRSAKeyPair(t)
	token, err := MintJWT(otherPrivatePEM, uuid.New(), uuid.New(), 3600)
	require.NoError(t, err)
	_, err = DecodeAccessToken(token, publicKey, 0)
	require.Error(t, err, "expected a token signed by a different key to fail verification")
}

func TestDecodeAccessTokenRejectsAnExpiredToken(t *testing.T) {
	privatePEM, publicKey := generateTestRSAKeyPair(t)
	// A zero/negative TTL mints an already-expired token.
	token, err := MintJWT(privatePEM, uuid.New(), uuid.New(), 0)
	require.NoError(t, err)
	time.Sleep(1100 * time.Millisecond)
	_, err = DecodeAccessToken(token, publicKey, 0)
	require.Error(t, err, "expected an expired token to fail verification")
}
