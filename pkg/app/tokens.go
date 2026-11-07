package app

import (
	"context"
	"crypto/rsa"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/metap-org/metap/pkg/infra"
	"github.com/metap-org/metap/pkg/jwks"
	"github.com/metap-org/metap/pkg/kit/env"
)

// TokenAuth is how a service signs and verifies tokens, resolved from config by BuildTokenAuth.
//
// Three modes:
//   - issuer (JWKS_KEYS_DIR): signs with the key directory's signing key, publishes
//     /.well-known/jwks.json, verifies its own tokens from memory. Exactly one service per
//     deployment should be the issuer.
//   - verifier (JWKS_URL): verifies against the issuer's JWKS, never signs — /auth/login there
//     answers 501 not_token_issuer.
//   - legacy (neither): the shared static RSA keypair; every service signs and verifies.
//
// In both JWKS modes AUTH_JWT_PUBLIC_KEY_PATH, when set, keeps verifying kid-less RS256 tokens
// already issued (a zero-downtime cutover); unset it once those have expired.
type TokenAuth struct {
	PrivateKeyPEM string         // legacy mode only
	PublicKey     *rsa.PublicKey // legacy mode, or the cutover fallback
	Signer        *jwks.TokenSigner
	Verifier      *jwks.TokenVerifier
	Keys          *jwks.SharedKeyStore // issuer only
}

// Mode names the resolved mode, for logs.
func (a *TokenAuth) Mode() string {
	switch {
	case a.Keys != nil:
		return "jwks-issuer"
	case a.Verifier != nil:
		return "jwks-verifier"
	default:
		return "static-rsa"
	}
}

const tokenLeeway = 20 * time.Second

// BuildTokenAuth resolves cfg into a TokenAuth. Background work (key-dir reload, JWKS refresh)
// runs until ctx is done.
func BuildTokenAuth(ctx context.Context, cfg *infra.AppConfig) (*TokenAuth, error) {
	auth := &TokenAuth{}
	if cfg.AuthJWTPublicKeyPath != "" {
		key, err := ReadPublicKey(cfg.AuthJWTPublicKeyPath)
		if err != nil {
			return nil, fmt.Errorf("reading AUTH_JWT_PUBLIC_KEY_PATH: %w", err)
		}
		rsaKey, ok := key.(*rsa.PublicKey)
		if !ok {
			return nil, errors.New("AUTH_JWT_PUBLIC_KEY_PATH is not an RSA public key")
		}
		auth.PublicKey = rsaKey
	}

	switch {
	case cfg.JWKSKeysDir != "":
		store, err := jwks.LoadKeyDir(cfg.JWKSKeysDir)
		if err != nil {
			return nil, err
		}
		auth.Keys = jwks.NewSharedKeyStore(store)
		auth.Signer = &jwks.TokenSigner{Kind: jwks.SignerStore, Store: auth.Keys}
		auth.Verifier = &jwks.TokenVerifier{Kind: jwks.VerifierAuto, Keys: auth.Keys, PublicKey: auth.PublicKey, Leeway: tokenLeeway}
		jwks.WatchKeyDir(ctx, cfg.JWKSKeysDir, auth.Keys, time.Duration(env.Int("JWKS_KEY_DIR_RELOAD_SECONDS", 30))*time.Second)
		if cfg.AuthJWTPrivateKeyPath != "" {
			slog.Warn("AUTH_JWT_PRIVATE_KEY_PATH is ignored: this service is the JWKS issuer and signs EdDSA only")
		}
	case cfg.JWKSURL != "":
		client := jwks.NewJwksClient(cfg.JWKSURL, time.Duration(env.Int("JWKS_CACHE_TTL_SECONDS", 300))*time.Second)
		client.SpawnBackgroundRefresh(ctx, time.Duration(env.Int("JWKS_REFRESH_SECONDS", 60))*time.Second)
		auth.Verifier = &jwks.TokenVerifier{Kind: jwks.VerifierAuto, Keys: client, PublicKey: auth.PublicKey, Leeway: tokenLeeway}
		if cfg.AuthJWTPrivateKeyPath != "" {
			slog.Warn("AUTH_JWT_PRIVATE_KEY_PATH is ignored: with JWKS_URL this service only verifies; the issuer signs")
		}
	default:
		pem, err := os.ReadFile(cfg.AuthJWTPrivateKeyPath)
		if err != nil {
			return nil, fmt.Errorf("reading AUTH_JWT_PRIVATE_KEY_PATH: %w", err)
		}
		if auth.PublicKey == nil {
			return nil, errors.New("AUTH_JWT_PUBLIC_KEY_PATH is required in static-RSA mode")
		}
		auth.PrivateKeyPEM = string(pem)
	}
	return auth, nil
}
