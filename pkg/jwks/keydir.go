package jwks

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Key directory layout (the issuer's JWKS_KEYS_DIR):
//
//	<kid>.key     raw 64-byte Ed25519 private key, one file per key — every one is published
//	signing-kid   the kid new tokens are signed with
//
// Rotation, with no restart (WatchKeyDir picks each step up):
//  1. add the next key (dev-tools gen-jwks-key <dir>) — published, not yet signing;
//  2. once every verifier has refreshed (> their JWKS cache TTL), point signing-kid at it;
//  3. once the longest-lived token signed by the old key has expired, delete the old .key file.
const (
	keyFileSuffix  = ".key"
	signingKidFile = "signing-kid"
)

var kidPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

// LoadKeyDir reads a key directory into a key store.
func LoadKeyDir(dir string) (*JwksKeyStore, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading JWKS key dir: %w", err)
	}
	var keys []*JwksKeyPair
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, keyFileSuffix) {
			continue
		}
		kid := strings.TrimSuffix(name, keyFileSuffix)
		if !kidPattern.MatchString(kid) {
			return nil, fmt.Errorf("JWKS key file %q: kid must match %s", name, kidPattern)
		}
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		key, err := FromPrivateKeyBytes(kid, ed25519.PrivateKey(raw))
		if err != nil {
			return nil, fmt.Errorf("JWKS key file %q: %w", name, err)
		}
		keys = append(keys, key)
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("JWKS key dir %s has no *%s files", dir, keyFileSuffix)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].Kid < keys[j].Kid })

	rawKid, err := os.ReadFile(filepath.Join(dir, signingKidFile))
	if err != nil {
		return nil, fmt.Errorf("JWKS key dir %s: reading %s: %w", dir, signingKidFile, err)
	}
	signingKid := strings.TrimSpace(string(rawKid))
	var signing *JwksKeyPair
	for _, k := range keys {
		if k.Kid == signingKid {
			signing = k
		}
	}
	if signing == nil {
		return nil, fmt.Errorf("JWKS key dir %s: %s names %q, which has no %s file", dir, signingKidFile, signingKid, keyFileSuffix)
	}
	store := NewJwksKeyStore(signing)
	for _, k := range keys {
		if k.Kid != signingKid {
			store.AddKey(k)
		}
	}
	return store, nil
}

// GenerateKeyFile creates a new key in dir (creating dir if needed). The first key in a
// directory also becomes the signing key; later ones are only published until signing-kid is
// pointed at them (rotation step 1).
func GenerateKeyFile(dir, kid string) (*JwksKeyPair, error) {
	if !kidPattern.MatchString(kid) {
		return nil, fmt.Errorf("kid %q must match %s", kid, kidPattern)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, kid+keyFileSuffix)
	if _, err := os.Stat(path); err == nil {
		return nil, fmt.Errorf("%s already exists", path)
	}
	key, err := GenerateJwksKeyPair(kid)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, key.PrivateKeyBytes(), 0o600); err != nil {
		return nil, err
	}
	signingPath := filepath.Join(dir, signingKidFile)
	if _, err := os.Stat(signingPath); os.IsNotExist(err) {
		if err := os.WriteFile(signingPath, []byte(kid+"\n"), 0o644); err != nil {
			return nil, err
		}
	}
	return key, nil
}

// WatchKeyDir reloads dir into shared every interval until ctx is done. A directory that fails to
// load (mid-edit, bad signing-kid) is logged and skipped; the previous key set stays in force.
func WatchKeyDir(ctx context.Context, dir string, shared *SharedKeyStore, interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				store, err := LoadKeyDir(dir)
				if err != nil {
					slog.Warn("JWKS key dir reload failed; keeping the current key set", "dir", dir, "error", err)
					continue
				}
				before := shared.SigningKey().Kid
				shared.Replace(store)
				if after := store.SigningKey().Kid; after != before {
					slog.Info("JWKS signing key rotated", "from", before, "to", after)
				}
			}
		}
	}()
}
