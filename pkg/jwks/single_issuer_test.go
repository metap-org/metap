package jwks

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/peripherals"
)

func rsaPair(t *testing.T) (string, *rsa.PublicKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})), &key.PublicKey
}

func sharedStore(t *testing.T, kid string) *SharedKeyStore {
	t.Helper()
	key, err := GenerateJwksKeyPair(kid)
	require.NoError(t, err)
	return NewSharedKeyStore(NewJwksKeyStore(key))
}

// countingServer serves whatever store currently publishes and counts fetches.
func countingServer(t *testing.T, store *SharedKeyStore) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_ = json.NewEncoder(w).Encode(store.JwkSet())
	}))
	t.Cleanup(server.Close)
	return server, &hits
}

func TestAutoVerifierRoutesByKidAndFallsBackToStaticRSAOnlyWhenEnabled(t *testing.T) {
	ctx := context.Background()
	store := sharedStore(t, "k1")
	privPEM, pub := rsaPair(t)

	eddsa, err := MintWithSigner(&TokenSigner{Kind: SignerStore, Store: store}, uuid.New(), uuid.New(), nil, 60)
	require.NoError(t, err)
	legacy, err := peripherals.MintJWT(privPEM, uuid.New(), uuid.New(), 60)
	require.NoError(t, err)
	require.True(t, HasKid(eddsa))
	require.False(t, HasKid(legacy))

	migrating := &TokenVerifier{Kind: VerifierAuto, Keys: store, PublicKey: pub, Leeway: time.Second}
	_, err = DecodeWithVerifier(ctx, eddsa, migrating, nil)
	require.NoError(t, err)
	_, err = DecodeWithVerifier(ctx, legacy, migrating, nil)
	require.NoError(t, err, "legacy RSA sessions keep working during the cutover")

	jwksOnly := &TokenVerifier{Kind: VerifierAuto, Keys: store, Leeway: time.Second}
	_, err = DecodeWithVerifier(ctx, eddsa, jwksOnly, nil)
	require.NoError(t, err)
	_, err = DecodeWithVerifier(ctx, legacy, jwksOnly, nil)
	require.Error(t, err, "once the RSA key is dropped, kid-less tokens are rejected")
}

func TestAutoVerifierRejectsATokenFromAnotherIssuer(t *testing.T) {
	ours, theirs := sharedStore(t, "k1"), sharedStore(t, "k1") // same kid, different key
	token, err := MintWithSigner(&TokenSigner{Kind: SignerStore, Store: theirs}, uuid.New(), uuid.New(), nil, 60)
	require.NoError(t, err)
	_, err = DecodeWithVerifier(context.Background(), token, &TokenVerifier{Kind: VerifierAuto, Keys: ours}, nil)
	require.Error(t, err)
}

func TestStoreSignerFollowsARotationWithoutBeingRebuilt(t *testing.T) {
	store := sharedStore(t, "old")
	signer := &TokenSigner{Kind: SignerStore, Store: store}
	next, err := GenerateJwksKeyPair("new")
	require.NoError(t, err)
	require.NoError(t, store.Mutate(func(s *JwksKeyStore) error { s.AddKey(next); return s.Promote("new") }))

	token, err := MintWithSigner(signer, uuid.New(), uuid.New(), nil, 60)
	require.NoError(t, err)
	_, err = DecodeEdDSA(context.Background(), token, next.publicKeySource(), time.Second)
	require.NoError(t, err)
}

func TestClientDropsARetiredKeyAtTheNextRefreshEvenIfItWasCached(t *testing.T) {
	ctx := context.Background()
	store := sharedStore(t, "a")
	token, err := MintWithSigner(&TokenSigner{Kind: SignerStore, Store: store}, uuid.New(), uuid.New(), nil, 600)
	require.NoError(t, err)
	server, _ := countingServer(t, store)
	client := NewJwksClient(server.URL, 50*time.Millisecond)
	client.MinRefreshInterval = 0

	_, err = client.Decode(ctx, token, time.Second)
	require.NoError(t, err)

	next, _ := GenerateJwksKeyPair("b")
	require.NoError(t, store.Mutate(func(s *JwksKeyStore) error {
		s.AddKey(next)
		if err := s.Promote("b"); err != nil {
			return err
		}
		return s.RemoveKey("a")
	}))
	time.Sleep(80 * time.Millisecond) // past the snapshot TTL

	_, err = client.Decode(ctx, token, time.Second)
	require.Error(t, err, "a key the issuer no longer publishes must stop verifying")
}

func TestClientThrottlesFetchesForUnknownKids(t *testing.T) {
	ctx := context.Background()
	store := sharedStore(t, "a")
	server, hits := countingServer(t, store)
	client := NewJwksClient(server.URL, time.Hour)

	attacker := sharedStore(t, "made-up")
	for i := 0; i < 20; i++ {
		token, err := MintWithSigner(&TokenSigner{Kind: SignerStore, Store: attacker}, uuid.New(), uuid.New(), nil, 60)
		require.NoError(t, err)
		_, err = client.Decode(ctx, token, time.Second)
		require.Error(t, err)
	}
	assert.EqualValues(t, 1, hits.Load(), "20 unknown-kid tokens must not become 20 fetches")
}

func TestClientKeepsVerifyingKnownKeysWhileTheIssuerIsDown(t *testing.T) {
	ctx := context.Background()
	store := sharedStore(t, "a")
	token, err := MintWithSigner(&TokenSigner{Kind: SignerStore, Store: store}, uuid.New(), uuid.New(), nil, 600)
	require.NoError(t, err)
	server, _ := countingServer(t, store)
	client := NewJwksClient(server.URL, 10*time.Millisecond)
	client.MinRefreshInterval = 0
	_, err = client.Decode(ctx, token, time.Second)
	require.NoError(t, err)

	server.Close()
	time.Sleep(20 * time.Millisecond)
	_, err = client.Decode(ctx, token, time.Second)
	require.NoError(t, err)
}

func TestKeyDirFirstKeySignsLaterKeysArePublishedUntilPromoted(t *testing.T) {
	dir := t.TempDir()
	_, err := GenerateKeyFile(dir, "k1")
	require.NoError(t, err)
	_, err = GenerateKeyFile(dir, "k2")
	require.NoError(t, err)

	store, err := LoadKeyDir(dir)
	require.NoError(t, err)
	assert.Equal(t, "k1", store.SigningKey().Kid)
	assert.Len(t, store.JwkSet().Keys, 2)

	require.NoError(t, os.WriteFile(filepath.Join(dir, "signing-kid"), []byte("k2\n"), 0o644))
	store, err = LoadKeyDir(dir)
	require.NoError(t, err)
	assert.Equal(t, "k2", store.SigningKey().Kid)
}

func TestKeyDirRejectsASigningKidWithNoKeyFile(t *testing.T) {
	dir := t.TempDir()
	_, err := GenerateKeyFile(dir, "k1")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "signing-kid"), []byte("missing"), 0o644))
	_, err = LoadKeyDir(dir)
	require.ErrorContains(t, err, "missing")

	_, err = GenerateKeyFile(dir, "../escape")
	require.Error(t, err)
}

func TestWatchKeyDirAppliesARotationAndIgnoresABrokenEdit(t *testing.T) {
	dir := t.TempDir()
	_, err := GenerateKeyFile(dir, "k1")
	require.NoError(t, err)
	store, err := LoadKeyDir(dir)
	require.NoError(t, err)
	shared := NewSharedKeyStore(store)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	WatchKeyDir(ctx, dir, shared, 10*time.Millisecond)

	require.NoError(t, os.WriteFile(filepath.Join(dir, "signing-kid"), []byte("nope"), 0o644))
	time.Sleep(40 * time.Millisecond)
	assert.Equal(t, "k1", shared.SigningKey().Kid, "a broken directory leaves the current key set in force")

	_, err = GenerateKeyFile(dir, "k2")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "signing-kid"), []byte("k2"), 0o644))
	require.Eventually(t, func() bool { return shared.SigningKey().Kid == "k2" }, time.Second, 10*time.Millisecond)
}

// publicKeySource is a one-key KeySource (tests and single-key verification).
func (k *JwksKeyPair) publicKeySource() KeySource { return singleKey{k} }

type singleKey struct{ k *JwksKeyPair }

func (s singleKey) PublicKeyFor(_ context.Context, kid string) (ed25519.PublicKey, error) {
	if kid != s.k.Kid {
		return nil, fmt.Errorf("unknown kid %s", kid)
	}
	return s.k.PublicKey(), nil
}
