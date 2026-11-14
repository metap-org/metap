// The issuer-only half of the JWKS trust model (see this package's own doc comment) — GET
// /.well-known/jwks.json, public, no auth (a JWKS document is public key material by definition,
// same trust model any OAuth2/OIDC provider's own jwks_uri uses). Only the one microservice
// nominated as the trust root's issuer mounts this; every other microservice only needs JwksClient
// (the verifying side), not this file.
// sharedStore := jwks.NewSharedKeyStore(keyStore)
// r.Get("/.well-known/jwks.json", jwks.Handler(sharedStore))
package jwks

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
)

// SharedKeyStore is the concurrency wrapper every caller of Handler needs — JwksKeyStore itself has
// no internal lock (rotation and the HTTP handler's reads are the binary's own concern to
// synchronize). RWMutex, not a plain Mutex — reads (every JWKS request) vastly outnumber writes (a
// rotation step), and a read lock lets concurrent requests actually serve in parallel.
type SharedKeyStore struct {
	mu    sync.RWMutex
	store *JwksKeyStore
}

func NewSharedKeyStore(store *JwksKeyStore) *SharedKeyStore {
	return &SharedKeyStore{store: store}
}

// JwkSet reads the current published key set under the read lock.
func (s *SharedKeyStore) JwkSet() JwkSet {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.store.JwkSet()
}

// Mutate runs a rotation step (AddKey/Promote/RemoveKey) under the write lock, blocking new
// reads until it returns.
func (s *SharedKeyStore) Mutate(fn func(*JwksKeyStore) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return fn(s.store)
}

// Handler serves the current JwkSet as JSON — a direct read-and-encode, no logic of its own beyond
// that. SigningKey is the key the issuer signs new tokens with right now.
func (s *SharedKeyStore) SigningKey() *JwksKeyPair {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.store.SigningKey()
}

// PublicKeyFor implements KeySource over every key currently published (signing or not), so an
// issuer verifies its own tokens — including ones signed by a key it is rotating away from —
// without an HTTP round trip to itself.
func (s *SharedKeyStore) PublicKeyFor(_ context.Context, kid string) (ed25519.PublicKey, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, k := range s.store.keys {
		if k.Kid == kid {
			return k.PublicKey(), nil
		}
	}
	return nil, fmt.Errorf("kid %s is not published by this issuer", kid)
}

// Replace swaps in a whole new key set at once (e.g. re-read from the key directory).
func (s *SharedKeyStore) Replace(store *JwksKeyStore) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.store = store
}

func Handler(store *SharedKeyStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(store.JwkSet())
	}
}
