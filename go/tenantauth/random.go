package tenantauth

import (
	"crypto/rand"
	"encoding/base64"
)

// generateRandomToken is this package's CSRF-token/nonce generator — the Go equivalent of the
// Rust openidconnect/oauth2 crates' CsrfToken::new_random()/Nonce::new_random(), which this
// package doesn't depend on, so it generates its own 256-bit random token the same way
// oauthserver.GenerateOpaqueToken does.
func generateRandomToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
