// Wraps a TenantRegistry with a short-TTL cache so Router.Begin (called on every tenant-scoped
// request) doesn't hit control.tenants per request — control.tenants changes rarely
// (provisioning, suspend/promote), so a 30s staleness window is an acceptable tradeoff against
// DB load.
package control

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	gocache "github.com/patrickmn/go-cache"
	"golang.org/x/sync/singleflight"
)

const registryCacheTTL = 30 * time.Second

// registryCacheEntry wraps a *TenantRouting so a cached "no row" result (nil) is distinguishable
// from "not yet cached at all" when read back from go-cache's untyped Get.
type registryCacheEntry struct {
	routing *TenantRouting
}

type RegistryCache struct {
	registry TenantRegistry
	cache    *gocache.Cache
	// group de-dupes concurrent misses for the same key (thundering herd) — the Go equivalent of
	// moka::future::Cache::try_get_with's own de-dup guarantee, which go-cache doesn't provide on
	// its own.
	group singleflight.Group
}

func NewRegistryCache(registry TenantRegistry) *RegistryCache {
	return &RegistryCache{registry: registry, cache: gocache.New(registryCacheTTL, registryCacheTTL)}
}

// Get is cache-first; on a miss it looks up the registry and caches the result — "no row" (nil)
// is cached too, since that lookup is exactly as expensive as a registered one and just as
// stable for the TTL window.
func (c *RegistryCache) Get(ctx context.Context, tenantID uuid.UUID) (*TenantRouting, error) {
	key := tenantID.String()
	if v, ok := c.cache.Get(key); ok {
		return v.(registryCacheEntry).routing, nil
	}

	v, err, _ := c.group.Do(key, func() (any, error) {
		// Re-check the cache: another goroutine may have populated it while we were waiting to
		// enter the singleflight call.
		if v, ok := c.cache.Get(key); ok {
			return v.(registryCacheEntry).routing, nil
		}
		routing, err := c.registry.Get(ctx, tenantID)
		if err != nil {
			return nil, err
		}
		c.cache.SetDefault(key, registryCacheEntry{routing: routing})
		return routing, nil
	})
	if err != nil {
		return nil, fmt.Errorf("tenant registry lookup failed for %s: %w", tenantID, err)
	}
	routing, _ := v.(*TenantRouting)
	return routing, nil
}
