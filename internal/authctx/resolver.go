package authctx

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/metap-org/metap/pkg/auth/identity"
	"github.com/metap-org/metap/pkg/kit/standard"
	"github.com/metap-org/metap/pkg/metadata"
	"github.com/metap-org/metap/pkg/permission"
	"github.com/metap-org/metap/pkg/tenancy"
)

// Resolver turns a verified token's claims into a permission.RequestContext: the DB-touching half of
// authentication that jwks.DecodeAccessToken (the pure JWT-verify half) deliberately cannot do itself.
type Resolver struct {
	Store Store
	// AuthContextEntity (empty: none) names the entity whose row for a member supplies extra attributes to
	// policy conditions; Registry resolves it to a table.
	AuthContextEntity string
	Registry          *atomic.Pointer[metadata.MetadataRegistry]
	// Attributes and Roles cache what they name; a nil Roles reads the roles on every request.
	Attributes *ContextAttributesCache
	Roles      *RolesCache
}

// Context builds a RequestContext for an already-verified token: the session check (always live), the
// roles (live, or reused for the RolesCache TTL) plus the AUTH_CONTEXT_ENTITY opt-in best-effort
// attributes lookup (cached). Any failure resolving roles propagates as an error (identity without roles is
// unusable), while a failure resolving context attributes only logs and yields nil (supplementary ABAC
// context, never blocks auth).
//
// issuedAt is the token's iat: a token for a member who was disabled or removed, or issued before their
// sessions were revoked, fails with identity.ErrDisabled / identity.ErrSessionRevoked.
func (r *Resolver) Context(ctx context.Context, tenantID, userID uuid.UUID, functionID string, issuedAt *time.Time) (*permission.RequestContext, error) {
	if err := r.Store.CheckSession(ctx, tenantID, userID, issuedAt); err != nil {
		return nil, err
	}
	held, err := r.Roles.get(ctx, tenantID, userID, func(ctx context.Context) (callerRoles, error) {
		h, err := r.Store.Roles(ctx, tenantID, userID)
		return callerRoles{roles: h.Roles, scoped: h.Scoped}, err
	})
	if err != nil {
		return nil, err
	}
	return &permission.RequestContext{
		TenantID:          tenantID.String(),
		UserID:            userID.String(),
		Roles:             held.roles,
		ScopedRoles:       held.scoped,
		HasRoles:          true,
		FunctionID:        functionID,
		ContextAttributes: r.attributes(ctx, tenantID, userID),
		// Not a forwarding source — this is the shared server-side "decode a token, look up roles" path
		// every static-verifier server runs, not a gateway's own inbound-request auth.
		ForwardedBearerToken: "",
	}, nil
}

// table is the table the AUTH_CONTEXT_ENTITY opt-in reads ("" when it is off or names nothing registered).
func (r *Resolver) table() string {
	if r.AuthContextEntity == "" || r.Registry == nil {
		return ""
	}
	reg := r.Registry.Load()
	if reg == nil {
		return ""
	}
	entity, ok := reg.GetEntity(r.AuthContextEntity)
	if !ok {
		slog.Warn("AUTH_CONTEXT_ENTITY does not resolve to a registered entity", "entity", r.AuthContextEntity)
		return ""
	}
	return entity.TableName
}

func (r *Resolver) attributes(ctx context.Context, tenantID, userID uuid.UUID) map[string]json.RawMessage {
	table := r.table()
	if table == "" {
		return nil
	}
	attrs, err := r.Attributes.GetWith(ctx, tenantID, userID, func(ctx context.Context) (map[string]any, error) {
		return r.Store.ContextAttributes(ctx, tenantID, table, userID)
	})
	if err != nil {
		slog.Warn("failed to resolve context attributes", standard.LogTenantID, tenantID, "user_id", userID, standard.LogError, err)
		return nil
	}
	return attributesJSON(attrs)
}

// attributesJSON is the attributes as raw JSON values; a value that cannot be encoded is left out. Pure.
func attributesJSON(attrs map[string]any) map[string]json.RawMessage {
	if attrs == nil {
		return nil
	}
	out := make(map[string]json.RawMessage, len(attrs))
	for k, v := range attrs {
		b, err := json.Marshal(v)
		if err != nil {
			continue
		}
		out[k] = b
	}
	return out
}

// APIKey authenticates a presented API key ("mk_...") and resolves the request context of the member it
// belongs to. The key counts as a session issued when it was created, so "revoke sessions" on the member
// kills older keys too, and a disabled member's keys stop working.
func (r *Resolver) APIKey(ctx context.Context, token string, now time.Time) (*permission.RequestContext, error) {
	key, err := r.Store.ResolveAPIKey(ctx, token, now)
	if err != nil {
		return nil, err
	}
	created := key.CreatedAt
	return r.Context(ctx, key.TenantID, key.MemberID, "", &created)
}

// ApplySession checks the login session behind a token's "sid" claim (roadmap 115 M4): a revoked or unknown
// session fails with identity.ErrSessionRevoked. A token with no "sid" is not a login session and passes
// untouched.
func (r *Resolver) ApplySession(ctx context.Context, rc *permission.RequestContext, sessionID *string, now time.Time) error {
	if sessionID == nil {
		return nil
	}
	sid, err := uuid.Parse(*sessionID)
	if err != nil {
		return identity.ErrSessionRevoked
	}
	tenant, err := uuid.Parse(rc.TenantID)
	if err != nil {
		return identity.ErrSessionRevoked
	}
	user, err := uuid.Parse(rc.UserID)
	if err != nil {
		return identity.ErrSessionRevoked
	}
	if err := r.Store.CheckSessionID(ctx, tenant, user, sid, now); err != nil {
		return err
	}
	rc.SessionID = *sessionID
	return nil
}

// The three functions below are the Resolver over the Postgres store, for callers that hold a router.

func pgResolver(router *tenancy.Router, authContextEntity string, registry *atomic.Pointer[metadata.MetadataRegistry],
	attrs *ContextAttributesCache, roles *RolesCache) *Resolver {
	return &Resolver{Store: NewPgStore(router), AuthContextEntity: authContextEntity, Registry: registry, Attributes: attrs, Roles: roles}
}

// ResolveRequestContext is Resolver.Context over the Postgres store.
func ResolveRequestContext(ctx context.Context, router *tenancy.Router, tenantID, userID uuid.UUID, functionID string, issuedAt *time.Time,
	authContextEntity string, metadataRegistry *atomic.Pointer[metadata.MetadataRegistry], contextAttributesCache *ContextAttributesCache,
	rolesCache *RolesCache) (*permission.RequestContext, error) {
	return pgResolver(router, authContextEntity, metadataRegistry, contextAttributesCache, rolesCache).Context(ctx, tenantID, userID, functionID, issuedAt)
}

// ResolveAPIKeyContext is Resolver.APIKey over the Postgres store.
func ResolveAPIKeyContext(ctx context.Context, router *tenancy.Router, token string, now time.Time,
	authContextEntity string, metadataRegistry *atomic.Pointer[metadata.MetadataRegistry], contextAttributesCache *ContextAttributesCache,
	rolesCache *RolesCache) (*permission.RequestContext, error) {
	return pgResolver(router, authContextEntity, metadataRegistry, contextAttributesCache, rolesCache).APIKey(ctx, token, now)
}

// ApplySessionClaim is Resolver.ApplySession over the Postgres store.
func ApplySessionClaim(ctx context.Context, router *tenancy.Router, rc *permission.RequestContext, sessionID *string, now time.Time) error {
	return pgResolver(router, "", nil, nil, nil).ApplySession(ctx, rc, sessionID, now)
}
