package jwks

// Ported from the Rust metap-jwks crate's src/lib.rs test module, using net/http/httptest in
// place of wiremock for the JwksClient tests.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

func TestAFreshlyGeneratedKeySignsAndVerifiesItself(t *testing.T) {
	key, err := GenerateJwksKeyPair("kid-1")
	if err != nil {
		t.Fatal(err)
	}
	tenantID, userID := uuid.New(), uuid.New()
	token, err := MintServiceOrUserJWT(key, tenantID, userID, nil, 3600)
	if err != nil {
		t.Fatal(err)
	}

	var wire jwksClaims
	_, err = jwt.ParseWithClaims(token, &wire, func(*jwt.Token) (any, error) { return key.PublicKey(), nil },
		jwt.WithValidMethods([]string{"EdDSA"}))
	if err != nil {
		t.Fatal(err)
	}
	if wire.Subject != userID.String() {
		t.Fatalf("got sub %q", wire.Subject)
	}
	if wire.TenantID != tenantID.String() {
		t.Fatalf("got tenantId %q", wire.TenantID)
	}
}

func TestATokenSignedByOneKeyDoesNotVerifyAgainstAnother(t *testing.T) {
	keyA, _ := GenerateJwksKeyPair("kid-a")
	keyB, _ := GenerateJwksKeyPair("kid-b")
	token, err := MintServiceOrUserJWT(keyA, uuid.New(), uuid.New(), nil, 3600)
	if err != nil {
		t.Fatal(err)
	}

	var wire jwksClaims
	_, err = jwt.ParseWithClaims(token, &wire, func(*jwt.Token) (any, error) { return keyB.PublicKey(), nil },
		jwt.WithValidMethods([]string{"EdDSA"}))
	if err == nil {
		t.Fatal("expected verification against the wrong key to fail")
	}
}

func TestJwkSetRoundTripsThroughJson(t *testing.T) {
	key, _ := GenerateJwksKeyPair("kid-1")
	store := NewJwksKeyStore(key)
	b, err := json.Marshal(store.JwkSet())
	if err != nil {
		t.Fatal(err)
	}
	var parsed JwkSet
	if err := json.Unmarshal(b, &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Keys) != 1 || parsed.Keys[0].Kty != "OKP" || parsed.Keys[0].Crv != "Ed25519" || parsed.Keys[0].Kid != "kid-1" {
		t.Fatalf("got %+v", parsed)
	}
}

func TestRotationPublishesTheNextKeyBeforeSigningWithIt(t *testing.T) {
	keyA, _ := GenerateJwksKeyPair("kid-a")
	store := NewJwksKeyStore(keyA)
	if store.SigningKey().Kid != "kid-a" {
		t.Fatal("expected kid-a to be signing")
	}

	keyB, _ := GenerateJwksKeyPair("kid-b")
	store.AddKey(keyB)
	if len(store.JwkSet().Keys) != 2 {
		t.Fatal("expected 2 published keys")
	}
	if store.SigningKey().Kid != "kid-a" {
		t.Fatal("expected kid-a to still be signing")
	}

	if err := store.Promote("kid-b"); err != nil {
		t.Fatal(err)
	}
	if store.SigningKey().Kid != "kid-b" {
		t.Fatal("expected kid-b to now be signing")
	}
	if len(store.JwkSet().Keys) != 2 {
		t.Fatal("expected the retired key to stay published during the grace window")
	}

	if err := store.RemoveKey("kid-a"); err != nil {
		t.Fatal(err)
	}
	if len(store.JwkSet().Keys) != 1 {
		t.Fatal("expected exactly 1 published key after removal")
	}
}

func TestPromotingAnUnpublishedKidFails(t *testing.T) {
	key, _ := GenerateJwksKeyPair("kid-a")
	store := NewJwksKeyStore(key)
	if err := store.Promote("does-not-exist"); err == nil {
		t.Fatal("expected an error")
	}
}

func TestRemovingTheSigningKeyFails(t *testing.T) {
	key, _ := GenerateJwksKeyPair("kid-a")
	store := NewJwksKeyStore(key)
	if err := store.RemoveKey("kid-a"); err == nil {
		t.Fatal("expected an error")
	}
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
	if err != nil {
		t.Fatal(err)
	}

	server := jwksTestServer(t, store)
	client := NewJwksClient(server.URL, 300*time.Second)
	claims, err := client.Decode(context.Background(), token, 20*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(claims.TenantID) != 36 {
		t.Fatalf("expected a UUID string, got %q", claims.TenantID)
	}
}

func TestJwksClientPicksUpANewlyRotatedInKeyOnCacheMiss(t *testing.T) {
	keyA, _ := GenerateJwksKeyPair("kid-a")
	store := NewJwksKeyStore(keyA)

	// Rotate in a second key before the client (created below) has ever fetched anything — its
	// first fetch, triggered by decoding tokenB, must see the *current* (post-rotation) JWKS.
	keyB, _ := GenerateJwksKeyPair("kid-b")
	store.AddKey(keyB)
	if err := store.Promote("kid-b"); err != nil {
		t.Fatal(err)
	}
	tokenB, err := MintServiceOrUserJWT(store.SigningKey(), uuid.New(), uuid.New(), nil, 3600)
	if err != nil {
		t.Fatal(err)
	}

	server := jwksTestServer(t, store)
	client := NewJwksClient(server.URL, 300*time.Second)
	claims, err := client.Decode(context.Background(), tokenB, 20*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(claims.Subject) != 36 {
		t.Fatalf("expected a UUID string, got %q", claims.Subject)
	}
}

// The other direction: a token signed by a key that has since been retired (no longer published
// in the issuer's JwkSet) must be rejected by a verifier that hasn't already cached that
// specific kid — a cache *miss* always re-fetches the current JWKS, and the retired kid is
// simply absent from it.
func TestJwksClientRejectsATokenSignedByAKeyNoLongerPublishedInTheJwks(t *testing.T) {
	keyA, _ := GenerateJwksKeyPair("kid-a")
	store := NewJwksKeyStore(keyA)
	tokenA, err := MintServiceOrUserJWT(store.SigningKey(), uuid.New(), uuid.New(), nil, 3600)
	if err != nil {
		t.Fatal(err)
	}

	// Full rotation lifecycle: a second key takes over signing, then the original is retired.
	keyB, _ := GenerateJwksKeyPair("kid-b")
	store.AddKey(keyB)
	if err := store.Promote("kid-b"); err != nil {
		t.Fatal(err)
	}
	if err := store.RemoveKey("kid-a"); err != nil {
		t.Fatal(err)
	}

	server := jwksTestServer(t, store)
	// A fresh client, never having cached kid-a — its first lookup is a cache miss, which always
	// fetches the issuer's *current* (post-retirement) JWKS.
	client := NewJwksClient(server.URL, 300*time.Second)
	if _, err := client.Decode(context.Background(), tokenA, 20*time.Second); err == nil {
		t.Fatal("expected a token signed by a retired, no-longer-published key to fail verification")
	}
}
