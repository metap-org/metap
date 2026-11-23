// PKCE (RFC 7636), S256 only — plain code_challenge_method=plain is not supported, since it
// offers no protection against an authorization code interception attack on a device that can
// read another app's redirect (the exact threat PKCE exists for).
package oauthserver

import (
	"crypto/sha256"
	"encoding/base64"
)

// VerifyPKCE reports whether codeVerifier hashes (SHA-256, base64url no-pad) to codeChallenge —
// RFC 7636 §4.6's S256 check. This function is only ever called when a challenge was stored (see
// ConsumeAuthorizationCode's own PKCE branch), so an absent verifier against a present challenge
// is always a caller error, not something this function decides.
func VerifyPKCE(codeVerifier, codeChallenge string) bool {
	digest := sha256.Sum256([]byte(codeVerifier))
	return base64.RawURLEncoding.EncodeToString(digest[:]) == codeChallenge
}
