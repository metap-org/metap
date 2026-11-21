package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// Keyring holds the application keys that seal tenant credentials stored in the database. The
// first key is the active one (new writes); the others only open rows sealed earlier, which is
// how a key is rotated: add the new key in front, re-save credentials at leisure, drop the old.
type Keyring struct {
	active string
	keys   map[string][]byte
}

// ParseKeyring reads "id:base64key[,id:base64key...]" — every key 32 bytes (AES-256).
func ParseKeyring(spec string) (*Keyring, error) {
	ring := &Keyring{keys: map[string][]byte{}}
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, encoded, ok := strings.Cut(part, ":")
		if !ok || id == "" {
			return nil, errors.New(`SECRETS_ENCRYPTION_KEYS entries must look like "id:base64key"`)
		}
		key, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, fmt.Errorf("SECRETS_ENCRYPTION_KEYS key %q is not valid base64: %w", id, err)
		}
		if len(key) != 32 {
			return nil, fmt.Errorf("SECRETS_ENCRYPTION_KEYS key %q is %d bytes, need 32 (AES-256)", id, len(key))
		}
		if _, dup := ring.keys[id]; dup {
			return nil, fmt.Errorf("SECRETS_ENCRYPTION_KEYS key id %q is repeated", id)
		}
		if ring.active == "" {
			ring.active = id
		}
		ring.keys[id] = key
	}
	if ring.active == "" {
		return nil, errors.New("SECRETS_ENCRYPTION_KEYS holds no key")
	}
	return ring, nil
}

// GenerateKey is a fresh random key, base64 as ParseKeyring reads it.
func GenerateKey() (string, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(key), nil
}

func (k *Keyring) gcm(id string) (cipher.AEAD, error) {
	key, ok := k.keys[id]
	if !ok {
		return nil, fmt.Errorf("no encryption key %q in SECRETS_ENCRYPTION_KEYS (rotated out too early?)", id)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// seal encrypts plaintext under the active key, binding it to ref.
func (k *Keyring) seal(ref string, plaintext []byte) (ciphertext, nonce []byte, keyID string, err error) {
	aead, err := k.gcm(k.active)
	if err != nil {
		return nil, nil, "", err
	}
	nonce = make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, "", err
	}
	return aead.Seal(nil, nonce, plaintext, []byte(ref)), nonce, k.active, nil
}

// open decrypts a row sealed under keyID for ref; any other ref (or a tampered row) fails.
func (k *Keyring) open(ref, keyID string, nonce, ciphertext []byte) ([]byte, error) {
	aead, err := k.gcm(keyID)
	if err != nil {
		return nil, err
	}
	plaintext, err := aead.Open(nil, nonce, ciphertext, []byte(ref))
	if err != nil {
		return nil, errors.New("stored credential could not be decrypted (wrong key, or the row was altered)")
	}
	return plaintext, nil
}
