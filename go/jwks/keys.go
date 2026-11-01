// Package jwks is a JWKS-based multi-service trust root — an alternative to the platform's
// default per-app static RS256 keypair (peripherals.MintJWT) for a deployment where several
// separately-deployed microservices need to trust the *same* signing identity: one nominated
// issuer binary holds the only private key(s), every other microservice verifies locally via a
// cached JwksClient. Ported from metap's Rust metap-jwks crate.
//
// metap-jwks-http (the HTTP surface serving GET /.well-known/jwks.json) is ported in http.go,
// right in this package rather than a separate one — a plain http.HandlerFunc has none of the
// axum Router<State>-parameterization friction that made the Rust original a whole separate
// crate.
//
// Ed25519 (EdDSA), not RS256, for every token this package mints — smaller tokens, faster verify
// than RS256. Divergence from the Rust original: that version works in PKCS8 DER bytes end to
// end (ring::signature::Ed25519KeyPair, jsonwebtoken::EncodingKey::from_ed_der) since Rust's
// jsonwebtoken crate wants DER-encoded keys; Go's crypto/ed25519 and golang-jwt/jwt v5 both work
// directly with ed25519.PrivateKey/PublicKey (raw 64/32-byte values), so there's no DER encoding
// step at all here — PrivateKeyBytes/FromPrivateKeyBytes carry the raw ed25519.PrivateKey instead
// of PKCS8, named differently from the Rust private_pkcs8/from_pkcs8 so a reader doesn't assume
// PKCS8-compatibility between the two ports' persisted key bytes.
package jwks

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
)

// JwksKeyPair is one Ed25519 signing identity: a kid (key id, carried in every JWT this key
// signs so a verifier holding several published keys knows which one to check against) plus the
// key material itself.
type JwksKeyPair struct {
	Kid        string
	privateKey ed25519.PrivateKey
	publicX    string // base64url-no-pad, the JWK "x" value
}

// GenerateJwksKeyPair generates a fresh Ed25519 keypair with the given kid. Callers mint a new
// kid per rotation (a UUID or a timestamp-derived string both work).
func GenerateJwksKeyPair(kid string) (*JwksKeyPair, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("failed to generate Ed25519 key: %w", err)
	}
	return &JwksKeyPair{Kid: kid, privateKey: priv, publicX: base64.RawURLEncoding.EncodeToString(pub)}, nil
}

// FromPrivateKeyBytes reconstructs a keypair from a previously generated raw Ed25519 private key
// (e.g. read back from a SecretStore) — the counterpart to GenerateJwksKeyPair for a process that
// didn't mint this key itself but needs to sign with it.
func FromPrivateKeyBytes(kid string, privateKey ed25519.PrivateKey) (*JwksKeyPair, error) {
	if len(privateKey) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("invalid Ed25519 private key: expected %d bytes, got %d", ed25519.PrivateKeySize, len(privateKey))
	}
	pub := privateKey.Public().(ed25519.PublicKey)
	return &JwksKeyPair{Kid: kid, privateKey: privateKey, publicX: base64.RawURLEncoding.EncodeToString(pub)}, nil
}

func (k *JwksKeyPair) PrivateKeyBytes() ed25519.PrivateKey { return k.privateKey }

func (k *JwksKeyPair) PublicKey() ed25519.PublicKey { return k.privateKey.Public().(ed25519.PublicKey) }

func (k *JwksKeyPair) Jwk() Jwk {
	return Jwk{Kty: "OKP", Crv: "Ed25519", Kid: k.Kid, Use: "sig", Alg: "EdDSA", X: k.publicX}
}

// Jwk is one entry of a JWKS document (RFC 7517) — only the OKP/Ed25519 shape this package ever
// mints.
type Jwk struct {
	Kty string `json:"kty"`
	Crv string `json:"crv"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	Alg string `json:"alg"`
	X   string `json:"x"`
}

type JwkSet struct {
	Keys []Jwk `json:"keys"`
}

// JwksKeyStore holds every key a verifier should currently trust, plus which one new tokens
// actually get signed with — the "keep 2-3 active keys" rotation model: a key is published
// (added here, appears in JwkSet) before it's ever used to sign anything, so every verifier's
// next JWKS refresh already has it cached by the time it needs to; conversely a retired key
// stays published for a grace window after signing stops, so a token minted right before
// rotation still verifies. This type only holds the in-memory rotation state — where the keys
// themselves persist across restarts is the issuing binary's own decision, not this package's.
type JwksKeyStore struct {
	keys       []*JwksKeyPair
	signingKid string
}

// NewJwksKeyStore starts a store with exactly one key, immediately both published and signing —
// the bootstrap case.
func NewJwksKeyStore(signingKey *JwksKeyPair) *JwksKeyStore {
	return &JwksKeyStore{keys: []*JwksKeyPair{signingKey}, signingKid: signingKey.Kid}
}

// AddKey is step 1 of rotation: publish a new key's public half without signing anything with it
// yet. Safe to call at any time — an unused published key is inert.
func (s *JwksKeyStore) AddKey(key *JwksKeyPair) {
	s.keys = append(s.keys, key)
}

// Promote is step 2 of rotation: switch signing to a key that's already published. Errors if kid
// isn't in the published set.
func (s *JwksKeyStore) Promote(kid string) error {
	for _, k := range s.keys {
		if k.Kid == kid {
			s.signingKid = kid
			return nil
		}
	}
	return fmt.Errorf("cannot promote %s: not in the published key set yet", kid)
}

// RemoveKey is step 3 of rotation: stop publishing a retired key. Call only after the grace
// window has passed since the key stopped signing. Refuses to remove the currently-signing key.
func (s *JwksKeyStore) RemoveKey(kid string) error {
	if kid == s.signingKid {
		return fmt.Errorf("cannot remove %s: it is the currently signing key — promote another key first", kid)
	}
	out := s.keys[:0]
	for _, k := range s.keys {
		if k.Kid != kid {
			out = append(out, k)
		}
	}
	s.keys = out
	return nil
}

func (s *JwksKeyStore) SigningKey() *JwksKeyPair {
	for _, k := range s.keys {
		if k.Kid == s.signingKid {
			return k
		}
	}
	panic("signingKid always points at a key present in keys")
}

func (s *JwksKeyStore) JwkSet() JwkSet {
	keys := make([]Jwk, len(s.keys))
	for i, k := range s.keys {
		keys[i] = k.Jwk()
	}
	return JwkSet{Keys: keys}
}
