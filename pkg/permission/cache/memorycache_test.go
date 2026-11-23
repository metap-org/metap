package cache

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestGetAfterSetReturnsTheValue(t *testing.T) {
	c := NewMemoryCache(30 * time.Second)
	ctx := context.Background()
	tenant := uuid.New()
	require.NoError(t, c.Set(ctx, tenant, "k", []byte("v")))
	v, ok, err := c.Get(ctx, tenant, "k")
	require.NoError(t, err, "got (%q, %v, %v)", v, ok, err)
	require.True(t, ok, "got (%q, %v, %v)", v, ok, err)
	require.Equal(t, "v", string(v), "got (%q, %v, %v)", v, ok, err)
}

func TestGetOnAMissingKeyIsOkFalse(t *testing.T) {
	c := NewMemoryCache(30 * time.Second)
	_, ok, err := c.Get(context.Background(), uuid.New(), "missing")
	require.NoError(t, err, "got (ok=%v, err=%v), want ok=false", ok, err)
	require.False(t, ok, "got (ok=%v, err=%v), want ok=false", ok, err)
}

func TestSameKeyDifferentTenantsNeverCollide(t *testing.T) {
	c := NewMemoryCache(30 * time.Second)
	ctx := context.Background()
	tenantA, tenantB := uuid.New(), uuid.New()
	_ = c.Set(ctx, tenantA, "k", []byte("a's value"))
	_ = c.Set(ctx, tenantB, "k", []byte("b's value"))
	va, _, _ := c.Get(ctx, tenantA, "k")
	vb, _, _ := c.Get(ctx, tenantB, "k")
	require.Equal(t, "a's value", string(va), "got (%q, %q)", va, vb)
	require.Equal(t, "b's value", string(vb), "got (%q, %q)", va, vb)
}

func TestInvalidateRemovesOnlyThatTenantsEntry(t *testing.T) {
	c := NewMemoryCache(30 * time.Second)
	ctx := context.Background()
	tenantA, tenantB := uuid.New(), uuid.New()
	_ = c.Set(ctx, tenantA, "k", []byte("a"))
	_ = c.Set(ctx, tenantB, "k", []byte("b"))
	_ = c.Invalidate(ctx, tenantA, "k")
	_, okA, _ := c.Get(ctx, tenantA, "k")
	vb, okB, _ := c.Get(ctx, tenantB, "k")
	require.False(t, okA, "tenant A's entry should have been invalidated")
	require.True(t, okB, "tenant B's entry should be untouched, got (%q, %v)", vb, okB)
	require.Equal(t, "b", string(vb), "tenant B's entry should be untouched, got (%q, %v)", vb, okB)
}

func TestEntriesExpireAfterTTL(t *testing.T) {
	c := NewMemoryCache(50 * time.Millisecond)
	ctx := context.Background()
	tenant := uuid.New()
	_ = c.Set(ctx, tenant, "k", []byte("v"))
	_, ok, _ := c.Get(ctx, tenant, "k")
	require.True(t, ok, "expected a hit immediately after set")
	time.Sleep(150 * time.Millisecond)
	_, ok, _ = c.Get(ctx, tenant, "k")
	require.False(t, ok, "entry must have expired")
}
