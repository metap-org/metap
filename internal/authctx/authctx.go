// Package authctx turns a verified access token's claims into a permission.RequestContext — the DB-touching
// half of authentication that jwks.DecodeAccessToken (the pure JWT-verify half) deliberately can't do
// itself. resolver.go is the logic (Resolver), store.go the port it reads through and its Postgres adapter,
// and this file the two caches (a member's roles, their context attributes) and the notices that clear them.
package authctx

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	gocache "github.com/patrickmn/go-cache"
	"golang.org/x/sync/singleflight"

	"github.com/metap-org/metap/pkg/permission"
)

type contextAttributesCacheEntry struct {
	attrs map[string]any
}

// Unlike role lookup (always fresh, never cached), this is cached: caller attributes come from an
// ordinary business record, not a security-critical role assignment, and the whole point of the
// AUTH_CONTEXT_ENTITY feature is to avoid a DB round-trip on every authenticated request when
// nothing about the caller's org membership actually changes between requests.
type ContextAttributesCache struct {
	cache *gocache.Cache
	group singleflight.Group
}

func NewContextAttributesCache(ttl time.Duration) *ContextAttributesCache {
	return &ContextAttributesCache{cache: gocache.New(ttl, ttl)}
}

// GetWith's fetch runs only on a cache miss — the actual records query, supplied by the caller
// so this type stays free of any entity-name knowledge. A nil result (no matching record) is
// cached too, so a user with no membership record doesn't re-query on every request either.
func (c *ContextAttributesCache) GetWith(ctx context.Context, tenantID, userID uuid.UUID, fetch func(ctx context.Context) (map[string]any, error)) (map[string]any, error) {
	key := tenantID.String() + "/" + userID.String()
	if v, ok := c.cache.Get(key); ok {
		return v.(contextAttributesCacheEntry).attrs, nil
	}
	v, err, _ := c.group.Do(key, func() (any, error) {
		if v, ok := c.cache.Get(key); ok {
			return v.(contextAttributesCacheEntry).attrs, nil
		}
		attrs, err := fetch(ctx)
		if err != nil {
			return nil, err
		}
		c.cache.SetDefault(key, contextAttributesCacheEntry{attrs: attrs})
		return attrs, nil
	})
	if err != nil {
		return nil, err
	}
	attrs, _ := v.(map[string]any)
	return attrs, nil
}

// Invalidate is a no-op if the key isn't cached — safe to call unconditionally.
func (c *ContextAttributesCache) Invalidate(tenantID, userID uuid.UUID) {
	c.cache.Delete(tenantID.String() + "/" + userID.String())
}

// callerRoles is what the cache holds for one member: their tenant-wide roles and the roles they hold only
// inside a scope (Held, as the store returns it).
type callerRoles struct {
	roles  []string
	scoped []permission.ScopedRole
}

// RolesCache keeps a member's resolved roles for a short time (roadmap 120: a role change or a
// revocation may apply late, by at most the TTL). What it never holds is the session: whether a
// token's session is revoked, or the member disabled, is checked on every request before the
// roles are read, so ending a session is still immediate.
//
// A nil *RolesCache is valid and caches nothing, so every request reads the roles fresh.
type RolesCache struct {
	cache *gocache.Cache
	group singleflight.Group
}

// NewRolesCache returns a cache holding roles for ttl; a ttl of zero or less returns nil, which
// reads the roles fresh on every request.
func NewRolesCache(ttl time.Duration) *RolesCache {
	if ttl <= 0 {
		return nil
	}
	return &RolesCache{cache: gocache.New(ttl, ttl)}
}

func (c *RolesCache) get(ctx context.Context, tenantID, userID uuid.UUID, fetch func(ctx context.Context) (callerRoles, error)) (callerRoles, error) {
	if c == nil {
		return fetch(ctx)
	}
	key := tenantID.String() + "/" + userID.String()
	if v, ok := c.cache.Get(key); ok {
		return v.(callerRoles).clone(), nil
	}
	v, err, _ := c.group.Do(key, func() (any, error) {
		if v, ok := c.cache.Get(key); ok {
			return v.(callerRoles), nil
		}
		r, err := fetch(ctx)
		if err != nil {
			return callerRoles{}, err
		}
		c.cache.SetDefault(key, r)
		return r, nil
	})
	if err != nil {
		return callerRoles{}, err
	}
	return v.(callerRoles).clone(), nil
}

// Invalidate drops one member's cached roles; a no-op when nothing is cached.
func (c *RolesCache) Invalidate(tenantID, userID uuid.UUID) {
	if c == nil {
		return
	}
	c.cache.Delete(tenantID.String() + "/" + userID.String())
}

// InvalidateTenant drops every cached member of one tenant (a scope went away, so any member's scoped
// roles may have changed); a no-op when nothing is cached.
func (c *RolesCache) InvalidateTenant(tenantID uuid.UUID) {
	if c == nil {
		return
	}
	prefix := tenantID.String() + "/"
	for key := range c.cache.Items() {
		if strings.HasPrefix(key, prefix) {
			c.cache.Delete(key)
		}
	}
}

// clone copies the slices so a caller that edits its RequestContext cannot change what the next
// request for the same member reads.
func (r callerRoles) clone() callerRoles {
	return callerRoles{roles: append([]string(nil), r.roles...), scoped: append([]permission.ScopedRole(nil), r.scoped...)}
}
