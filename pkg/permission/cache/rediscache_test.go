//go:build integration

package cache

// Runs only against a real Redis-protocol server (REDIS_URL), under the `integration` build tag.

import (
	"context"
	"testing"
	"time"

	"github.com/metap-org/metap/pkg/testkit"
	"github.com/stretchr/testify/require"

	"github.com/google/uuid"
)

func testRedisCache(t *testing.T, ttl time.Duration) *RedisCache {
	t.Helper()
	c, err := NewRedisCache(testkit.RedisURL(t), ttl)
	require.NoError(t, err, "connect to Redis/DragonflyDB")
	return c
}

func TestPutGetDeleteRoundTripAgainstRealRedis(t *testing.T) {
	c := testRedisCache(t, 30*time.Second)
	ctx := context.Background()
	tenant := uuid.New()
	_ = c.Set(ctx, tenant, "k", []byte("v"))
	v, ok, _ := c.Get(ctx, tenant, "k")
	require.True(t, ok, "got (%q, %v)", v, ok)
	require.Equal(t, "v", string(v), "got (%q, %v)", v, ok)
	_ = c.Invalidate(ctx, tenant, "k")
	_, ok, _ = c.Get(ctx, tenant, "k")
	require.False(t, ok, "expected a miss after invalidate")
}

func TestRedisSameKeyDifferentTenantsNeverCollide(t *testing.T) {
	c := testRedisCache(t, 30*time.Second)
	ctx := context.Background()
	tenantA, tenantB := uuid.New(), uuid.New()
	_ = c.Set(ctx, tenantA, "k", []byte("a"))
	_ = c.Set(ctx, tenantB, "k", []byte("b"))
	va, _, _ := c.Get(ctx, tenantA, "k")
	vb, _, _ := c.Get(ctx, tenantB, "k")
	require.Equal(t, "a", string(va), "got (%q, %q)", va, vb)
	require.Equal(t, "b", string(vb), "got (%q, %q)", va, vb)
	_ = c.Invalidate(ctx, tenantA, "k")
	_ = c.Invalidate(ctx, tenantB, "k")
}

func TestRedisEntriesExpireAfterTTL(t *testing.T) {
	c := testRedisCache(t, time.Second)
	ctx := context.Background()
	tenant := uuid.New()
	_ = c.Set(ctx, tenant, "k", []byte("v"))
	_, ok, _ := c.Get(ctx, tenant, "k")
	require.True(t, ok, "expected a hit immediately after set")
	time.Sleep(1500 * time.Millisecond)
	_, ok, _ = c.Get(ctx, tenant, "k")
	require.False(t, ok, "entry must have expired")
}
