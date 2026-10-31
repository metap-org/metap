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

	"github.com/metap-org/metap-go/metadata"
	"github.com/metap-org/metap-go/peripherals"
	"github.com/metap-org/metap-go/permission"
)

type contextAttributesCacheEntry struct {
	attrs map[string]any
}

// ContextAttributesCache mirrors the Rust ContextAttributesCache (moka::future::Cache, TTL,
// try_get_with to de-dupe concurrent misses). Unlike role lookup (always fresh, never cached),
// this is cached: caller attributes come from an ordinary business record, not a
// security-critical role assignment, and the whole point of the AUTH_CONTEXT_ENTITY feature is
// to avoid a DB round-trip on every authenticated request when nothing about the caller's org
// membership actually changes between requests.
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
//
// metadataRegistry is an *atomic.Pointer[metadata.MetadataRegistry], the Go equivalent of the
// Rust original's ArcSwap<MetadataRegistry> — both exist so a future hot-swap of the registry
// (no restart needed) is reflected here without this function needing to change.
func ResolveRequestContext(ctx context.Context, router *Router, tenantID, userID uuid.UUID, functionID string,
	authContextEntity string, metadataRegistry *atomic.Pointer[metadata.MetadataRegistry], contextAttributesCache *ContextAttributesCache) (*permission.RequestContext, error) {
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
			slog.Warn("failed to resolve context attributes", "tenant_id", tenantID, "user_id", userID, "error", err)
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
		HasRoles:          true,
		FunctionID:        functionID,
		ContextAttributes: contextAttributesJSON,
		// Not a forwarding source — this is the shared server-side "decode a token, look up
		// roles" path every static-verifier server runs, not a gateway's own inbound-request
		// auth.
		ForwardedBearerToken: "",
	}, nil
}
