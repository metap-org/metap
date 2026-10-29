// The verifying side of the JWKS trust model: fetches the issuer's published JwkSet over HTTP,
// caches keys by kid, and decodes/validates tokens against whichever key a token's own header
// names. Every microservice that wants to accept a portal/service token signed by the shared
// trust root holds one of these.
package jwks

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/metap-org/metap-go/peripherals"
	"github.com/metap-org/metap-go/runtime"
	gocache "github.com/patrickmn/go-cache"
)

// JwksClient's cache-miss behavior is "fetch the whole JWKS document, repopulate every key"
// rather than a per-kid fetch — a JWKS document is one small JSON payload, so there is no
// benefit to a narrower fetch, and repopulating every key on any miss means a verifier that's
// fallen behind by more than one rotation still catches up in a single request instead of
// needing one miss per newly-unseen kid.
type JwksClient struct {
	http    *http.Client
	jwksURL string
	cache   *gocache.Cache
}

func NewJwksClient(jwksURL string, ttl time.Duration) *JwksClient {
	return &JwksClient{
		http:    runtime.DefaultHTTPClient(),
		jwksURL: jwksURL,
		cache:   gocache.New(ttl, ttl),
	}
}

// refresh fetches the issuer's current JwkSet and repopulates the cache — used both by
// decodingKeyFor's miss path and by the periodic background refresh (SpawnBackgroundRefresh), so
// a key that's been rotated *out* is eventually evicted here too, not just a rotated-*in* key
// picked up.
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
	for _, jwk := range jwkSet.Keys {
		raw, err := base64.RawURLEncoding.DecodeString(jwk.X)
		if err != nil {
			return err
		}
		c.cache.SetDefault(jwk.Kid, ed25519.PublicKey(raw))
	}
	return nil
}

// decodingKeyFor is cache-first; on a miss, refreshes from the issuer once before giving up —
// this is what makes a freshly rotated-in key usable immediately (the first verifier to see a
// token signed with it just pays one extra HTTP round-trip, no coordinated deploy needed).
func (c *JwksClient) decodingKeyFor(ctx context.Context, kid string) (ed25519.PublicKey, error) {
	if v, ok := c.cache.Get(kid); ok {
		return v.(ed25519.PublicKey), nil
	}
	if err := c.refresh(ctx); err != nil {
		return nil, err
	}
	if v, ok := c.cache.Get(kid); ok {
		return v.(ed25519.PublicKey), nil
	}
	return nil, fmt.Errorf("kid %s not found in JWKS at %s", kid, c.jwksURL)
}

// Decode verifies a token minted by MintServiceOrUserJWT — reads the token's kid header to pick
// the right key (falling back to a fetch on a cache miss), then validates exactly like
// peripherals.DecodeAccessToken (JWTAudience/JWTIssuer, leeway clock-skew tolerance), except
// EdDSA instead of RS256.
func (c *JwksClient) Decode(ctx context.Context, tokenString string, leeway time.Duration) (*peripherals.AccessClaims, error) {
	unverified, _, err := jwt.NewParser().ParseUnverified(tokenString, jwt.MapClaims{})
	if err != nil {
		return nil, err
	}
	kidRaw, ok := unverified.Header["kid"]
	if !ok {
		return nil, fmt.Errorf("token has no kid — cannot select a JWKS verification key")
	}
	kid, _ := kidRaw.(string)

	key, err := c.decodingKeyFor(ctx, kid)
	if err != nil {
		return nil, err
	}

	var wire jwksClaims
	_, err = jwt.ParseWithClaims(tokenString, &wire, func(t *jwt.Token) (any, error) { return key, nil },
		jwt.WithValidMethods([]string{"EdDSA"}),
		jwt.WithAudience(peripherals.JWTAudience),
		jwt.WithIssuer(peripherals.JWTIssuer),
		jwt.WithLeeway(leeway),
	)
	if err != nil {
		return nil, err
	}

	var jti *string
	if wire.ID != "" {
		jti = &wire.ID
	}
	return &peripherals.AccessClaims{
		Subject:    wire.Subject,
		TenantID:   wire.TenantID,
		FunctionID: wire.FunctionID,
		JTI:        jti,
		Scope:      wire.Scope,
		ClientID:   wire.ClientID,
	}, nil
}

// SpawnBackgroundRefresh is an opt-in background refresh so a verifier that never misses still
// eventually notices that a key was rotated out — without this, decodingKeyFor alone would keep
// trusting a retired key forever once cached, since a cache *hit* never re-checks the issuer.
// Callers spawn this once at boot; cancel ctx on shutdown.
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
