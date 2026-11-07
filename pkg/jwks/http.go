// The issuer-only half of the JWKS trust model (see this package's own doc comment) —
// GET /.well-known/jwks.json, public, no auth (a JWKS document is public key material by
// definition, same trust model any OAuth2/OIDC provider's own jwks_uri uses). Only the one
// microservice nominated as the trust root's issuer mounts this; every other microservice only
// needs JwksClient (the verifying side), not this file. Ported from metap-jwks-http — closes the
// gap that crate's absence used to leave (this package's own top doc comment used to flag it as
// "trivial to add once [an HTTP framework] decision is made"; httpserver's chi-based router is
// that decision).
//
// Unlike the Rust original's axum Router<()>-vs-Router<AppState> workaround (axum 0.8 refuses to
// route_service a foreign Router directly into another one, only nest_service), a plain
// http.HandlerFunc has no such type-parameterization to fight — Handler mounts on any
// chi.Router/http.ServeMux exactly like any other handler:
//
//	sharedStore := jwks.NewSharedKeyStore(keyStore)
//	r.Get("/.well-known/jwks.json", jwks.Handler(sharedStore))
package jwks

import (
	"encoding/json"
	"net/http"
	"sync"
)

// SharedKeyStore is the concurrency wrapper every caller of Handler needs — JwksKeyStore itself
// has no internal lock (rotation and the HTTP handler's reads are the binary's own concern to
// synchronize), mirroring the Rust Arc<RwLock<JwksKeyStore>> type alias this wraps. RWMutex, not
// a plain Mutex — reads (every JWKS request) vastly outnumber writes (a rotation step), and a
// read lock lets concurrent requests actually serve in parallel.
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

// Handler serves the current JwkSet as JSON — a direct read-and-encode, no logic of its own
// beyond that, matching the Rust original's own "3-line handler" framing.
func Handler(store *SharedKeyStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(store.JwkSet())
	}
}
