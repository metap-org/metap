package cache

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	gocache "github.com/patrickmn/go-cache"
)

// MemoryCache is an in-process Cache (ported from metap-cache's MokaCache) — fine for a
// single-instance deployment; a horizontally scaled one should use RedisCache instead, with no
// change to any caller.
type MemoryCache struct {
	inner *gocache.Cache
}

// NewMemoryCache builds a MemoryCache with a fixed entry TTL.
func NewMemoryCache(ttl time.Duration) *MemoryCache {
	// Cleanup interval of ttl*2 is a reasonable default sweep cadence; entries are still never
	// returned once expired regardless of when the sweep runs (go-cache checks expiry on Get).
	return &MemoryCache{inner: gocache.New(ttl, ttl*2)}
}

func (c *MemoryCache) scopedKey(tenantID uuid.UUID, key string) (string, error) {
	if err := validateKey(key); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s:%s", tenantID, key), nil
}

func (c *MemoryCache) Get(_ context.Context, tenantID uuid.UUID, key string) ([]byte, bool, error) {
	scoped, err := c.scopedKey(tenantID, key)
	if err != nil {
		return nil, false, err
	}
	v, ok := c.inner.Get(scoped)
	if !ok {
		return nil, false, nil
	}
	return v.([]byte), true, nil
}

func (c *MemoryCache) Set(_ context.Context, tenantID uuid.UUID, key string, value []byte) error {
	scoped, err := c.scopedKey(tenantID, key)
	if err != nil {
		return err
	}
	c.inner.SetDefault(scoped, value)
	return nil
}

func (c *MemoryCache) Invalidate(_ context.Context, tenantID uuid.UUID, key string) error {
	scoped, err := c.scopedKey(tenantID, key)
	if err != nil {
		return err
	}
	c.inner.Delete(scoped)
	return nil
}
