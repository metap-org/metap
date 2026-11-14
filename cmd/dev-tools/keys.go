package main

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/metap-org/metap/pkg/jwks"
	"github.com/metap-org/metap/pkg/kit/env"
	"github.com/metap-org/metap/pkg/peripherals"
)

// genKeys mirrors generate-dev-jwt-keypair.mjs: an RSA 2048 keypair via the openssl CLI
// (already a tested dependency of this repo's dev workflow), not a new Go crypto dependency for
// something this infrequent.
func genKeys(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	privatePath := dir + "/dev-jwt-private.pem"
	publicPath := dir + "/dev-jwt-public.pem"

	if out, err := exec.Command("openssl", "genrsa", "-out", privatePath, "2048").CombinedOutput(); err != nil {
		return fmt.Errorf("openssl genrsa failed: %w\n%s", err, out)
	}
	if out, err := exec.Command("openssl", "rsa", "-in", privatePath, "-pubout", "-out", publicPath).CombinedOutput(); err != nil {
		return fmt.Errorf("openssl rsa -pubout failed: %w\n%s", err, out)
	}

	fmt.Printf("Generated dev JWT keypair in ./%s (gitignored).\n", dir)
	fmt.Printf("Set in .env: AUTH_JWT_PUBLIC_KEY_PATH=./%s/dev-jwt-public.pem\n", dir)
	return nil
}

// genJwksKey adds a new Ed25519 key to a JWKS key directory (the issuer's JWKS_KEYS_DIR, see
// jwks.LoadKeyDir for the layout). The first key in a directory becomes the signing key; a later
// one is only published until signing-kid is pointed at it — rotation step 1.
func genJwksKey(dir, kid string) error {
	if _, err := jwks.GenerateKeyFile(dir, kid); err != nil {
		return err
	}
	signing, err := os.ReadFile(dir + "/signing-kid")
	if err != nil {
		return err
	}
	fmt.Printf("Added JWKS Ed25519 key %s to %s (gitignored). Signing key: %s", kid, dir, signing)
	fmt.Printf("Issuer: JWKS_KEYS_DIR=%s. To rotate: wait past every verifier's JWKS_CACHE_TTL_SECONDS, then `echo %s > %s/signing-kid`.\n", dir, kid, dir)
	return nil
}

// mintToken mirrors mint-dev-token.mjs: same default tenant/user IDs, same 1h expiry, same
// keys/dev-jwt-private.pem path (relative to cwd). Delegates the actual encoding to
// peripherals.MintJWT — the same function POST /auth/login calls — so this CLI and a real login
// can't mint differently-shaped tokens.
//
// The default tenant/user ids are not a registered control.tenants row — calling this with no
// args against an app whose only real tenant is a dedicated_db one mints a token for a tenant
// Router has never heard of, which falls back to the shared platform schema and every request
// then fails with a confusing DB error instead of an auth error. No file I/O here to check
// registration against, so the fix is a loud warning on the default path rather than a silent
// trap.
func mintToken(args []string) error {
	var tenantID string
	if len(args) > 2 {
		tenantID = args[2]
	} else {
		fmt.Fprintln(os.Stderr, "Warning: no tenantId given, defaulting to 00000000-0000-0000-0000-000000000001 "+
			"— this is NOT a provisioned tenant for most apps (check the app's .env for its real tenant id). "+
			"Pass it explicitly: dev-tools mint-token <tenantId> [userId]")
		tenantID = "00000000-0000-0000-0000-000000000001"
	}
	userID := argOr(args, 3, "00000000-0000-0000-0000-000000000002")

	tenant, err := parseUUID(tenantID)
	if err != nil {
		return err
	}
	user, err := parseUUID(userID)
	if err != nil {
		return err
	}

	// JWKS_KEYS_DIR (the issuer's key directory) mints an EdDSA token exactly as the issuer would.
	if dir, ok := env.Lookup("JWKS_KEYS_DIR"); ok {
		store, err := jwks.LoadKeyDir(dir)
		if err != nil {
			return err
		}
		token, err := jwks.MintWithSigner(&jwks.TokenSigner{Kind: jwks.SignerStore, Store: jwks.NewSharedKeyStore(store)}, tenant, user, nil, 3600)
		if err != nil {
			return err
		}
		fmt.Println(token)
		return nil
	}

	// AUTH_JWT_PRIVATE_KEY_PATH, if set, wins over the plain keys/dev-jwt-private.pem relative-
	// to-cwd default — every other binary in this workspace already reads this exact env var.
	keyPath, ok := env.Lookup("AUTH_JWT_PRIVATE_KEY_PATH")
	if !ok {
		keyPath = "keys/dev-jwt-private.pem"
	}
	privatePEM, err := os.ReadFile(keyPath)
	if err != nil {
		return fmt.Errorf("failed to read %s: %w", keyPath, err)
	}

	token, err := peripherals.MintJWT(string(privatePEM), tenant, user, 3600)
	if err != nil {
		return err
	}
	fmt.Println(token)
	return nil
}
