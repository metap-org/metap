package httpx

import (
	"crypto/sha256"
	"crypto/subtle"
	"strings"
)

// BearerToken extracts the token from an Authorization header value ("Bearer <token>"). It
// returns ("", false) when the scheme is missing or the token is empty.
func BearerToken(header string) (string, bool) {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return "", false
	}
	token := strings.TrimSpace(header[len(prefix):])
	if token == "" {
		return "", false
	}
	return token, true
}

// SecretEqual compares a presented shared secret with the expected one in constant time. Both
// sides are hashed first so the comparison does not leak the secret's length either. An empty
// expected secret never matches: "no secret configured" must be decided by the caller, not
// satisfied by an empty header.
func SecretEqual(presented, expected string) bool {
	if expected == "" {
		return false
	}
	a := sha256.Sum256([]byte(presented))
	b := sha256.Sum256([]byte(expected))
	return subtle.ConstantTimeCompare(a[:], b[:]) == 1
}
