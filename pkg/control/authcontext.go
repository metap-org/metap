// Turns a verified access token's claims into a permission.RequestContext — the DB-touching
// half of authentication that peripherals.DecodeAccessToken (the pure JWT-verify half)
// deliberately can't do itself, since it needs a tenant-scoped Router to open the transaction
// GetRolesForUser/FetchContextAttributes run against.
package control

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	gocache "github.com/patrickmn/go-cache"
	"golang.org/x/sync/singleflight"

	"github.com/metap-org/metap/pkg/identity"
	"github.com/metap-org/metap/pkg/kit/standard"
	"github.com/metap-org/metap/pkg/metadata"
	"github.com/metap-org/metap/pkg/peripherals"
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

// ResolveRequestContext builds a RequestContext for an already-verified token: live role lookup
// (never cached) plus the AUTH_CONTEXT_ENTITY opt-in best-effort attributes lookup (cached via
// contextAttributesCache). Any failure resolving roles propagates as an error (identity without
// roles is unusable), while a failure resolving context attributes only logs and yields nil
// (supplementary ABAC context, never blocks auth).
// metadataRegistry is an *atomic.Pointer[metadata.MetadataRegistry].
//
// issuedAt is the token's iat: a token for a member who was disabled or removed, or issued before
// their sessions were revoked, fails with identity.ErrDisabled / identity.ErrSessionRevoked.
func ResolveRequestContext(ctx context.Context, router *Router, tenantID, userID uuid.UUID, functionID string, issuedAt *time.Time,
	authContextEntity string, metadataRegistry *atomic.Pointer[metadata.MetadataRegistry], contextAttributesCache *ContextAttributesCache) (*permission.RequestContext, error) {
	if err := identity.CheckSession(ctx, router.sharedPool, tenantID, userID, issuedAt); err != nil {
		return nil, err
	}
	// Routed through Router, not a bare pool — a DedicatedDb-strategy tenant's user_roles table
	// lives only in that tenant's own database.
	tx, err := router.Begin(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	roles, err := peripherals.GetRolesForUser(ctx, tx, tenantID, userID)
	if err != nil {
		tx.Rollback(ctx)
		return nil, err
	}
	// Roles held only inside a scope (roadmap 115 M1), from the same transaction and so the same
	// freshness as the roles above: never cached.
	scopedRoles, err := peripherals.ScopedRolesForUser(ctx, tx, tenantID, userID)
	if err != nil {
		tx.Rollback(ctx)
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	var authContextTable string
	if authContextEntity != "" {
		if reg := metadataRegistry.Load(); reg != nil {
			if entity, ok := reg.GetEntity(authContextEntity); ok {
				authContextTable = entity.TableName
			} else {
				slog.Warn("AUTH_CONTEXT_ENTITY does not resolve to a registered entity", "entity", authContextEntity)
			}
		}
	}

	var contextAttributes map[string]any
	if authContextTable != "" {
		contextAttributes, err = contextAttributesCache.GetWith(ctx, tenantID, userID, func(ctx context.Context) (map[string]any, error) {
			tx, err := router.Begin(ctx, tenantID)
			if err != nil {
				return nil, err
			}
			result, err := peripherals.FetchContextAttributes(ctx, tx, tenantID, authContextTable, userID)
			if err != nil {
				tx.Rollback(ctx)
				return nil, err
			}
			return result, tx.Commit(ctx)
		})
		if err != nil {
			slog.Warn("failed to resolve context attributes", standard.LogTenantID, tenantID, "user_id", userID, standard.LogError, err)
			contextAttributes = nil
		}
	}

	var contextAttributesJSON map[string]json.RawMessage
	if contextAttributes != nil {
		contextAttributesJSON = make(map[string]json.RawMessage, len(contextAttributes))
		for k, v := range contextAttributes {
			b, err := json.Marshal(v)
			if err != nil {
				continue
			}
			contextAttributesJSON[k] = b
		}
	}

	return &permission.RequestContext{
		TenantID:          tenantID.String(),
		UserID:            userID.String(),
		Roles:             roles,
		ScopedRoles:       scopedRoles,
		HasRoles:          true,
		FunctionID:        functionID,
		ContextAttributes: contextAttributesJSON,
		// Not a forwarding source — this is the shared server-side "decode a token, look up
		// roles" path every static-verifier server runs, not a gateway's own inbound-request
		// auth.
		ForwardedBearerToken: "",
	}, nil
}

// ResolveAPIKeyContext authenticates a presented API key ("mk_...") and resolves the request
// context of the member it belongs to. The key counts as a session issued when it was created, so
// "revoke sessions" on the member kills older keys too, and a disabled member's keys stop working.
func ResolveAPIKeyContext(ctx context.Context, router *Router, token string, now time.Time,
	authContextEntity string, metadataRegistry *atomic.Pointer[metadata.MetadataRegistry], contextAttributesCache *ContextAttributesCache) (*permission.RequestContext, error) {
	key, err := identity.ResolveAPIKey(ctx, router.sharedPool, token, now)
	if err != nil {
		return nil, err
	}
	created := key.CreatedAt
	return ResolveRequestContext(ctx, router, key.TenantID, key.MemberID, "", &created,
		authContextEntity, metadataRegistry, contextAttributesCache)
}
