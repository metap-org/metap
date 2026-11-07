// Package cache is a swappable cache — ported from metap's Rust metap-cache crate. Same shape as
// metap-storage's ObjectStore / metap-control's SecretStore: one interface, callers hold a
// Cache, never the concrete type.
//
// Security-first, not caller discipline: every method takes tenantID as a mandatory first
// parameter — a cache key collision across tenants would leak one tenant's data into another's
// response.
//
// What this must never cache: role/user_roles data — roles are looked up fresh on every request,
// never cached, so a revocation takes effect on the very next request.
package cache

import (
	"context"
	"errors"
	"unicode"

	"github.com/google/uuid"
)

// Cache is the swappable interface — MemoryCache (in-process) and RedisCache (distributed) both
// implement it.
type Cache interface {
	Get(ctx context.Context, tenantID uuid.UUID, key string) ([]byte, bool, error)
	// Set has no per-call ttl parameter — a Cache instance has one fixed TTL chosen at
	// construction. One cache = one kind of data with one appropriate expiry.
	Set(ctx context.Context, tenantID uuid.UUID, key string, value []byte) error
	// Invalidate on a key that isn't cached is not an error.
	Invalidate(ctx context.Context, tenantID uuid.UUID, key string) error
}

// validateKey is lighter than metap-storage's key validation — a cache key never becomes a
// URL/path, and every key here is built by trusted internal code, never derived from request
// input. Still checked, not trusted blindly.
func validateKey(key string) error {
	if key == "" {
		return errors.New("cache key must not be empty")
	}
	if len(key) > 256 {
		return errors.New("cache key must be at most 256 bytes")
	}
	for _, r := range key {
		if unicode.IsControl(r) {
			return errors.New("cache key must not contain control characters")
		}
	}
	return nil
}
