package jwks

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestAFreshlyGeneratedKeySignsAndVerifiesItself(t *testing.T) {
	key, err := GenerateJwksKeyPair("kid-1")
	require.NoError(t, err)
	tenantID, userID := uuid.New(), uuid.New()
	token, err := MintServiceOrUserJWT(key, tenantID, userID, nil, 3600)
	require.NoError(t, err)

	var wire jwksClaims
	_, err = jwt.ParseWithClaims(token, &wire, func(*jwt.Token) (any, error) { return key.PublicKey(), nil },
		jwt.WithValidMethods([]string{"EdDSA"}))
	require.NoError(t, err)
	require.Equal(t, userID.String(), wire.Subject, "got sub %q", wire.Subject)
	require.Equal(t, tenantID.String(), wire.TenantID, "got tenantId %q", wire.TenantID)
}

func TestATokenSignedByOneKeyDoesNotVerifyAgainstAnother(t *testing.T) {
	keyA, _ := GenerateJwksKeyPair("kid-a")
	keyB, _ := GenerateJwksKeyPair("kid-b")
	token, err := MintServiceOrUserJWT(keyA, uuid.New(), uuid.New(), nil, 3600)
	require.NoError(t, err)

	var wire jwksClaims
	_, err = jwt.ParseWithClaims(token, &wire, func(*jwt.Token) (any, error) { return keyB.PublicKey(), nil },
		jwt.WithValidMethods([]string{"EdDSA"}))
	require.Error(t, err, "expected verification against the wrong key to fail")
}

func TestJwkSetRoundTripsThroughJson(t *testing.T) {
	key, _ := GenerateJwksKeyPair("kid-1")
	store := NewJwksKeyStore(key)
	b, err := json.Marshal(store.JwkSet())
	require.NoError(t, err)
	var parsed JwkSet
	require.NoError(t, json.Unmarshal(b, &parsed))
	require.Len(t, parsed.Keys, 1, "got %+v", parsed)
	require.Equal(t, "OKP", parsed.Keys[0].Kty, "got %+v", parsed)
	require.Equal(t, "Ed25519", parsed.Keys[0].Crv, "got %+v", parsed)
	require.Equal(t, "kid-1", parsed.Keys[0].Kid, "got %+v", parsed)
}

func TestRotationPublishesTheNextKeyBeforeSigningWithIt(t *testing.T) {
	keyA, _ := GenerateJwksKeyPair("kid-a")
	store := NewJwksKeyStore(keyA)
	require.Equal(t, "kid-a", store.SigningKey().Kid, "expected kid-a to be signing")

	keyB, _ := GenerateJwksKeyPair("kid-b")
	store.AddKey(keyB)
	require.Len(t, store.JwkSet().Keys, 2, "expected 2 published keys")
	require.Equal(t, "kid-a", store.SigningKey().Kid, "expected kid-a to still be signing")

	require.NoError(t, store.Promote("kid-b"))
	require.Equal(t, "kid-b", store.SigningKey().Kid, "expected kid-b to now be signing")
	require.Len(t, store.JwkSet().Keys, 2, "expected the retired key to stay published during the grace window")

	require.NoError(t, store.RemoveKey("kid-a"))
	require.Len(t, store.JwkSet().Keys, 1, "expected exactly 1 published key after removal")
}

func TestPromotingAnUnpublishedKidFails(t *testing.T) {
	key, _ := GenerateJwksKeyPair("kid-a")
	store := NewJwksKeyStore(key)
	err := store.Promote("does-not-exist")
	require.Error(t, err, "expected an error")
}

func TestRemovingTheSigningKeyFails(t *testing.T) {
	key, _ := GenerateJwksKeyPair("kid-a")
	store := NewJwksKeyStore(key)
	err := store.RemoveKey("kid-a")
	require.Error(t, err, "expected an error")
}

func jwksTestServer(t *testing.T, store *JwksKeyStore) *httptest.Server {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(store.JwkSet())
	}))
	t.Cleanup(server.Close)
	return server
}

func TestJwksClientVerifiesATokenAfterFetchingTheIssuersJwkSet(t *testing.T) {
	key, _ := GenerateJwksKeyPair("kid-1")
	store := NewJwksKeyStore(key)
	token, err := MintServiceOrUserJWT(store.SigningKey(), uuid.New(), uuid.New(), nil, 3600)
	require.NoError(t, err)

	server := jwksTestServer(t, store)
	client := NewJwksClient(server.URL, 300*time.Second)
	claims, err := client.Decode(context.Background(), token, 20*time.Second)
	require.NoError(t, err)
	require.Equal(t, 36, len(claims.TenantID), "expected a UUID string, got %q", claims.TenantID)
}

func TestJwksClientPicksUpANewlyRotatedInKeyOnCacheMiss(t *testing.T) {
	keyA, _ := GenerateJwksKeyPair("kid-a")
	store := NewJwksKeyStore(keyA)

	// Rotate in a second key before the client (created below) has ever fetched anything — its
	// first fetch, triggered by decoding tokenB, must see the *current* (post-rotation) JWKS.
	keyB, _ := GenerateJwksKeyPair("kid-b")
	store.AddKey(keyB)
	require.NoError(t, store.Promote("kid-b"))
	tokenB, err := MintServiceOrUserJWT(store.SigningKey(), uuid.New(), uuid.New(), nil, 3600)
	require.NoError(t, err)

	server := jwksTestServer(t, store)
	client := NewJwksClient(server.URL, 300*time.Second)
	claims, err := client.Decode(context.Background(), tokenB, 20*time.Second)
	require.NoError(t, err)
	require.Equal(t, 36, len(claims.Subject), "expected a UUID string, got %q", claims.Subject)
}

// The other direction: a token signed by a key that has since been retired (no longer published
// in the issuer's JwkSet) must be rejected by a verifier that hasn't already cached that
// specific kid — a cache *miss* always re-fetches the current JWKS, and the retired kid is
// simply absent from it.
func TestJwksClientRejectsATokenSignedByAKeyNoLongerPublishedInTheJwks(t *testing.T) {
	keyA, _ := GenerateJwksKeyPair("kid-a")
	store := NewJwksKeyStore(keyA)
	tokenA, err := MintServiceOrUserJWT(store.SigningKey(), uuid.New(), uuid.New(), nil, 3600)
	require.NoError(t, err)

	// Full rotation lifecycle: a second key takes over signing, then the original is retired.
	keyB, _ := GenerateJwksKeyPair("kid-b")
	store.AddKey(keyB)
	require.NoError(t, store.Promote("kid-b"))
	require.NoError(t, store.RemoveKey("kid-a"))

	server := jwksTestServer(t, store)
	// A fresh client, never having cached kid-a — its first lookup is a cache miss, which always
	// fetches the issuer's *current* (post-retirement) JWKS.
	client := NewJwksClient(server.URL, 300*time.Second)
	_, err = client.Decode(context.Background(), tokenA, 20*time.Second)
	require.Error(t, err, "expected a token signed by a retired, no-longer-published key to fail verification")
}
