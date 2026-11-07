package app

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writePEM(t *testing.T, blockType string, der []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "key.pem")
	require.NoError(t, os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: der}), 0o600))
	return path
}

func TestReadPublicKeyAcceptsPKIXAndPKCS1RSA(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	pkix, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	require.NoError(t, err)

	for name, path := range map[string]string{
		"pkix":  writePEM(t, "PUBLIC KEY", pkix),
		"pkcs1": writePEM(t, "RSA PUBLIC KEY", x509.MarshalPKCS1PublicKey(&key.PublicKey)),
	} {
		t.Run(name, func(t *testing.T) {
			got, err := ReadPublicKey(path)
			require.NoError(t, err)
			rsaKey, ok := got.(*rsa.PublicKey)
			require.True(t, ok)
			assert.True(t, rsaKey.Equal(&key.PublicKey))
		})
	}
}

func TestReadPublicKeyRejectsNonPEM(t *testing.T) {
	path := filepath.Join(t.TempDir(), "junk")
	require.NoError(t, os.WriteFile(path, []byte("not a key"), 0o600))
	_, err := ReadPublicKey(path)
	require.Error(t, err)
}
