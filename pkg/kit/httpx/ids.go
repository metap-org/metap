package httpx

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// randomHex returns n random bytes as lowercase hex (2n characters).
func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("httpx: crypto/rand failed: %v", err)) // the OS entropy source is gone
	}
	return hex.EncodeToString(b)
}

// newUUID returns a random (version 4) UUID string, for opaque request and trace ids.
func newUUID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("httpx: crypto/rand failed: %v", err))
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
