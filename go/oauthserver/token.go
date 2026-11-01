// Opaque bearer-credential generation/hashing shared by client secrets, authorization codes and
// refresh tokens — one implementation so the three can't drift on entropy source or digest
// choice.
package oauthserver

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
)

// GenerateOpaqueToken returns 256 bits of OS-RNG randomness, base64url-(no-pad)-encoded into an
// opaque, URL-safe string. Used for a client secret, an authorization code, and a refresh token —
// all server-generated, never user-chosen, which is what makes the fast digest in HashToken the
// right choice instead of a slow password hash.
//
// Divergence from the Rust original: that version builds the 32 bytes from two concatenated
// uuid::Uuid::new_v4()s (chosen there to avoid a new RNG dependency, since the uuid crate's v4
// feature was already in the dependency tree) — this port reads crypto/rand directly instead,
// which is simpler, equally OS-RNG-backed, and avoids the ~6 fixed version/variant bits a v4 UUID
// spends out of its 128, so if anything this is marginally higher-entropy, never lower. The output
// shape (43-char base64url string) is identical either way.
func GenerateOpaqueToken() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}

// HashToken is a SHA-256 hex digest — deliberately not argon2/bcrypt. Every caller hashes a
// server-generated, high-entropy (256-bit) opaque token, never a human-chosen password, so the
// offline-brute-force threat a slow password hash defends against doesn't apply here; a fast
// digest is the correct and standard choice for this class of secret, and matters because the
// token endpoint looks one of these up on every single call, not just at login.
func HashToken(raw string) string {
	digest := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(digest[:])
}
