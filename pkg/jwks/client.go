// The verifying side of the JWKS trust model: fetches the issuer's published JwkSet over HTTP
// and decodes/validates tokens against whichever key a token's own header names. Every service
// that accepts tokens from an issuer it doesn't run in-process holds one of these.
package jwks

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/metap-org/metap/pkg/peripherals"
	"github.com/metap-org/metap/pkg/runtime"
)

// DefaultMinRefreshInterval bounds how often a lookup of an unknown kid may trigger a fetch:
// without it, every request carrying a made-up kid would become one request to the issuer.
const DefaultMinRefreshInterval = 5 * time.Second

// JwksClient holds a snapshot of the issuer's keys. The snapshot is replaced wholesale on every
// refresh (always the whole document, never per kid), so a verifier that fell several rotations
// behind catches up in one request, and a key the issuer has retired stops verifying at the next
// refresh instead of lingering. A refresh happens when the snapshot is older than ttl, on a
// lookup of a kid the snapshot lacks (at most once per MinRefreshInterval), or on
// SpawnBackgroundRefresh's timer.
type JwksClient struct {
	http    *http.Client
	jwksURL string
	ttl     time.Duration

	// MinRefreshInterval: see DefaultMinRefreshInterval. Zero disables the limit.
	MinRefreshInterval time.Duration

	mu          sync.RWMutex
	keys        map[string]ed25519.PublicKey
	fetchedAt   time.Time
	lastAttempt time.Time
}

// NewJwksClient returns a client for jwksURL whose key snapshot is refetched once older than ttl.
func NewJwksClient(jwksURL string, ttl time.Duration) *JwksClient {
	return &JwksClient{
		http:               runtime.DefaultHTTPClient(),
		jwksURL:            jwksURL,
		ttl:                ttl,
		MinRefreshInterval: DefaultMinRefreshInterval,
	}
}

func (c *JwksClient) refresh(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.jwksURL, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("jwks fetch from %s returned status %d", c.jwksURL, resp.StatusCode)
	}
	var jwkSet JwkSet
	if err := json.NewDecoder(resp.Body).Decode(&jwkSet); err != nil {
		return err
	}
	keys := make(map[string]ed25519.PublicKey, len(jwkSet.Keys))
	for _, jwk := range jwkSet.Keys {
		if jwk.Kty != "OKP" || jwk.Crv != "Ed25519" {
			continue
		}
		raw, err := base64.RawURLEncoding.DecodeString(jwk.X)
		if err != nil || len(raw) != ed25519.PublicKeySize {
			return fmt.Errorf("jwks from %s: malformed key %q", c.jwksURL, jwk.Kid)
		}
		keys[jwk.Kid] = ed25519.PublicKey(raw)
	}
	c.mu.Lock()
	c.keys, c.fetchedAt = keys, time.Now()
	c.mu.Unlock()
	return nil
}

// PublicKeyFor implements KeySource.
func (c *JwksClient) PublicKeyFor(ctx context.Context, kid string) (ed25519.PublicKey, error) {
	c.mu.RLock()
	key, known := c.keys[kid]
	fresh := c.keys != nil && time.Since(c.fetchedAt) < c.ttl
	c.mu.RUnlock()
	if known && fresh {
		return key, nil
	}

	c.mu.Lock()
	throttled := c.MinRefreshInterval > 0 && !c.lastAttempt.IsZero() && time.Since(c.lastAttempt) < c.MinRefreshInterval
	if !throttled {
		c.lastAttempt = time.Now()
	}
	c.mu.Unlock()
	if !throttled {
		// A failed refresh leaves the previous snapshot in place: an issuer outage must not
		// start rejecting every token that was verifying a moment ago.
		if err := c.refresh(ctx); err != nil && !known {
			return nil, err
		}
	}

	c.mu.RLock()
	defer c.mu.RUnlock()
	if key, ok := c.keys[kid]; ok {
		return key, nil
	}
	return nil, fmt.Errorf("kid %s not found in JWKS at %s", kid, c.jwksURL)
}

// Decode verifies an EdDSA token against the issuer's published keys.
func (c *JwksClient) Decode(ctx context.Context, tokenString string, leeway time.Duration) (*peripherals.AccessClaims, error) {
	return DecodeEdDSA(ctx, tokenString, c, leeway)
}

// SpawnBackgroundRefresh refetches the key set every interval until ctx is done, so a verifier
// whose lookups always hit the snapshot still learns about retired keys promptly.
func (c *JwksClient) SpawnBackgroundRefresh(ctx context.Context, interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = c.refresh(ctx)
			}
		}
	}()
}
