package cache

// Runs only against a real Redis-protocol server (REDIS_URL) — same opt-in e2e convention the
// Rust #[ignore] tests use. Skipped by default so `go test ./...` doesn't need Redis running.

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
)

func testRedisCache(t *testing.T, ttl time.Duration) *RedisCache {
	url := os.Getenv("REDIS_URL")
	if url == "" {
		t.Skip("REDIS_URL not set — skipping real-Redis e2e test")
	}
	c, err := NewRedisCache(url, ttl)
	if err != nil {
		t.Fatalf("connect to Redis/DragonflyDB: %v", err)
	}
	return c
}

func TestPutGetDeleteRoundTripAgainstRealRedis(t *testing.T) {
	c := testRedisCache(t, 30*time.Second)
	ctx := context.Background()
	tenant := uuid.New()
	_ = c.Set(ctx, tenant, "k", []byte("v"))
	v, ok, _ := c.Get(ctx, tenant, "k")
	if !ok || string(v) != "v" {
		t.Fatalf("got (%q, %v)", v, ok)
	}
	_ = c.Invalidate(ctx, tenant, "k")
	_, ok, _ = c.Get(ctx, tenant, "k")
	if ok {
		t.Fatal("expected a miss after invalidate")
	}
}

func TestRedisSameKeyDifferentTenantsNeverCollide(t *testing.T) {
	c := testRedisCache(t, 30*time.Second)
	ctx := context.Background()
	tenantA, tenantB := uuid.New(), uuid.New()
	_ = c.Set(ctx, tenantA, "k", []byte("a"))
	_ = c.Set(ctx, tenantB, "k", []byte("b"))
	va, _, _ := c.Get(ctx, tenantA, "k")
	vb, _, _ := c.Get(ctx, tenantB, "k")
	if string(va) != "a" || string(vb) != "b" {
		t.Fatalf("got (%q, %q)", va, vb)
	}
	_ = c.Invalidate(ctx, tenantA, "k")
	_ = c.Invalidate(ctx, tenantB, "k")
}

func TestRedisEntriesExpireAfterTTL(t *testing.T) {
	c := testRedisCache(t, time.Second)
	ctx := context.Background()
	tenant := uuid.New()
	_ = c.Set(ctx, tenant, "k", []byte("v"))
	_, ok, _ := c.Get(ctx, tenant, "k")
	if !ok {
		t.Fatal("expected a hit immediately after set")
	}
	time.Sleep(1500 * time.Millisecond)
	_, ok, _ = c.Get(ctx, tenant, "k")
	if ok {
		t.Fatal("entry must have expired")
	}
}
