package cache

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestGetAfterSetReturnsTheValue(t *testing.T) {
	c := NewMemoryCache(30 * time.Second)
	ctx := context.Background()
	tenant := uuid.New()
	if err := c.Set(ctx, tenant, "k", []byte("v")); err != nil {
		t.Fatal(err)
	}
	v, ok, err := c.Get(ctx, tenant, "k")
	if err != nil || !ok || string(v) != "v" {
		t.Fatalf("got (%q, %v, %v)", v, ok, err)
	}
}

func TestGetOnAMissingKeyIsOkFalse(t *testing.T) {
	c := NewMemoryCache(30 * time.Second)
	_, ok, err := c.Get(context.Background(), uuid.New(), "missing")
	if err != nil || ok {
		t.Fatalf("got (ok=%v, err=%v), want ok=false", ok, err)
	}
}

func TestSameKeyDifferentTenantsNeverCollide(t *testing.T) {
	c := NewMemoryCache(30 * time.Second)
	ctx := context.Background()
	tenantA, tenantB := uuid.New(), uuid.New()
	_ = c.Set(ctx, tenantA, "k", []byte("a's value"))
	_ = c.Set(ctx, tenantB, "k", []byte("b's value"))
	va, _, _ := c.Get(ctx, tenantA, "k")
	vb, _, _ := c.Get(ctx, tenantB, "k")
	if string(va) != "a's value" || string(vb) != "b's value" {
		t.Fatalf("got (%q, %q)", va, vb)
	}
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
	if okA {
		t.Fatal("tenant A's entry should have been invalidated")
	}
	if !okB || string(vb) != "b" {
		t.Fatalf("tenant B's entry should be untouched, got (%q, %v)", vb, okB)
	}
}

func TestEntriesExpireAfterTTL(t *testing.T) {
	c := NewMemoryCache(50 * time.Millisecond)
	ctx := context.Background()
	tenant := uuid.New()
	_ = c.Set(ctx, tenant, "k", []byte("v"))
	_, ok, _ := c.Get(ctx, tenant, "k")
	if !ok {
		t.Fatal("expected a hit immediately after set")
	}
	time.Sleep(150 * time.Millisecond)
	_, ok, _ = c.Get(ctx, tenant, "k")
	if ok {
		t.Fatal("entry must have expired")
	}
}
