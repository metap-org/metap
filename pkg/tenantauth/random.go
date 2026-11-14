package tenantauth

import (
	"crypto/rand"
	"encoding/base64"
)

// generateRandomToken is this package's CSRF-token/nonce generator.
func generateRandomToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
