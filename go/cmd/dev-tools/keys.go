package main

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/metap-org/metap-go/jwks"
	"github.com/metap-org/metap-go/peripherals"
	"github.com/metap-org/metap-go/runtime"
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

// genJwksKey generates an Ed25519 keypair for jwks's trust root — the multi-service alternative
// to genKeys's per-app static RS256 keypair. Writes 2 files a host binary reads back at boot:
// the raw 64-byte Ed25519 private key (dev-jwks-private.bin — a divergence from the Rust
// original's dev-jwks-private.der: this port's jwks.FromPrivateKeyBytes works directly with
// ed25519.PrivateKey's raw bytes, no PKCS8 DER encoding step, see that package's own doc comment)
// and the key id, plain text (dev-jwks-kid.txt).
func genJwksKey(dir, kid string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	privatePath := dir + "/dev-jwks-private.bin"
	kidPath := dir + "/dev-jwks-kid.txt"

	key, err := jwks.GenerateJwksKeyPair(kid)
	if err != nil {
		return err
	}
	if err := os.WriteFile(privatePath, key.PrivateKeyBytes(), 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(kidPath, []byte(kid), 0o644); err != nil {
		return err
	}

	fmt.Printf("Generated dev JWKS Ed25519 key (kid=%s) in ./%s (gitignored).\n", kid, dir)
	fmt.Printf("Set in .env: JWKS_PRIVATE_KEY_PATH=./%s/dev-jwks-private.bin, JWKS_KID_PATH=./%s/dev-jwks-kid.txt\n", dir, dir)
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

	// AUTH_JWT_PRIVATE_KEY_PATH, if set, wins over the plain keys/dev-jwt-private.pem relative-
	// to-cwd default — every other binary in this workspace already reads this exact env var.
	keyPath, ok := runtime.Optional("AUTH_JWT_PRIVATE_KEY_PATH")
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
