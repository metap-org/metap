package identity

import (
	"sync"

	"github.com/alexedwards/argon2id"
)

var argon2idParams = &argon2id.Params{
	Memory:      19456,
	Iterations:  2,
	Parallelism: 1,
	SaltLength:  16,
	KeyLength:   32,
}

// HashPassword is the argon2id hash every stored password uses.
func HashPassword(password string) (string, error) {
	return argon2id.CreateHash(password, argon2idParams)
}

// VerifyPassword checks password against a HashPassword hash.
func VerifyPassword(password, hash string) (bool, error) {
	return argon2id.ComparePasswordAndHash(password, hash)
}

var dummyHash = sync.OnceValues(func() (string, error) {
	return HashPassword("dummy-password-for-timing-safety")
})

// verifyDummyPassword spends the same argon2 cost as a real check, for a caller that found no
// account to check against — so a missing email can't be told apart by response time.
func verifyDummyPassword(password string) {
	if h, err := dummyHash(); err == nil {
		_, _ = VerifyPassword(password, h)
	}
}
