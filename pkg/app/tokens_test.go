package app

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/infra"
	"github.com/metap-org/metap/pkg/jwks"
)

func rsaFiles(t *testing.T) (privPath, pubPath string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	dir := t.TempDir()
	privPath, pubPath = filepath.Join(dir, "priv.pem"), filepath.Join(dir, "pub.pem")
	require.NoError(t, os.WriteFile(privPath, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0o600))
	pub, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(pubPath, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pub}), 0o644))
	return privPath, pubPath
}

func TestBuildTokenAuthStaticRSAModeNeedsBothKeys(t *testing.T) {
	priv, pub := rsaFiles(t)
	auth, err := BuildTokenAuth(context.Background(), &infra.AppConfig{AuthJWTPrivateKeyPath: priv, AuthJWTPublicKeyPath: pub})
	require.NoError(t, err)
	assert.Equal(t, "static-rsa", auth.Mode())
	assert.NotEmpty(t, auth.PrivateKeyPEM)
	assert.Nil(t, auth.Signer)
	assert.Nil(t, auth.Verifier)

	_, err = BuildTokenAuth(context.Background(), &infra.AppConfig{AuthJWTPrivateKeyPath: priv})
	require.Error(t, err)
}

func TestBuildTokenAuthIssuerSignsWithTheKeyDirAndNeverWithRSA(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir := t.TempDir()
	_, err := jwks.GenerateKeyFile(dir, "k1")
	require.NoError(t, err)
	priv, pub := rsaFiles(t)

	auth, err := BuildTokenAuth(ctx, &infra.AppConfig{JWKSKeysDir: dir, AuthJWTPrivateKeyPath: priv, AuthJWTPublicKeyPath: pub})
	require.NoError(t, err)
	assert.Equal(t, "jwks-issuer", auth.Mode())
	assert.Empty(t, auth.PrivateKeyPEM, "the issuer never signs RS256")
	require.NotNil(t, auth.Keys)
	assert.Equal(t, jwks.SignerStore, auth.Signer.Kind)
	assert.Equal(t, jwks.VerifierAuto, auth.Verifier.Kind)
	assert.NotNil(t, auth.Verifier.PublicKey, "the RSA public key stays as the cutover fallback")
}

func TestBuildTokenAuthVerifierNeverSigns(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	auth, err := BuildTokenAuth(ctx, &infra.AppConfig{JWKSURL: "http://issuer.invalid/.well-known/jwks.json"})
	require.NoError(t, err)
	assert.Equal(t, "jwks-verifier", auth.Mode())
	assert.Nil(t, auth.Signer)
	assert.Empty(t, auth.PrivateKeyPEM)
	assert.Nil(t, auth.Verifier.PublicKey, "no RSA fallback unless AUTH_JWT_PUBLIC_KEY_PATH is set")
}

func TestBuildTokenAuthRejectsABrokenKeyDir(t *testing.T) {
	_, err := BuildTokenAuth(context.Background(), &infra.AppConfig{JWKSKeysDir: t.TempDir()})
	require.Error(t, err)
}
