// Package httpserver is metap-http: the REST entity CRUD router plus minimal session auth. A
// deliberate, user-directed divergence from the Rust original (ported in full elsewhere in this
// session) — the Rust metap-http removed REST entity CRUD entirely and made GraphQL the only
// entity-facing transport (see ../CLAUDE.md's "REST entity CRUD removed, GraphQL-only now"
// section). This Go port skips GraphQL altogether and reinstates REST /api/{entity}* CRUD
// instead (routes_records.go, recovered from git history at the commit before that removal and
// ported essentially unchanged), since a from-scratch Go rewrite is not bound by that decision.
// Admin/platform routes (users/policies/cron/dashboards/config/oauth) are deferred to a later
// wave — this package covers only /health, /auth/*, and /api/{entity}*.
package httpserver

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/metap-org/metap-go/config"
	"github.com/metap-org/metap-go/control"
	"github.com/metap-org/metap-go/crud"
	"github.com/metap-org/metap-go/metadata"
	"github.com/metap-org/metap-go/peripherals"
	"github.com/metap-org/metap-go/permission"
	"github.com/metap-org/metap-go/runtime"
	"github.com/metap-org/metap-go/storage"
)

// AppState mirrors the Rust AppState — the subset this core wave's routes actually touch.
// Fields matching the Rust original's "None unless the host binary opts in" shape (TokenVerifier/
// TokenSigner for the metap-jwks trust root, MetadataResolver, AttachmentTables, object storage)
// are intentionally not ported yet — every field here starts at the same default the Rust
// AppState::new produces, and a caller may still set CookieSecure/AuthContextEntity/
// MetadataResolver directly (all fields exported) once those features are needed.
type AppState struct {
	Pool *pgxpool.Pool
	// Router is the multi-tenant seam — every tenant-scoped query goes through
	// Router.Begin(ctx, tenantID), never Pool directly.
	Router *control.Router
	// MetadataBase: code-authored entities only, fixed after boot.
	MetadataBase *metadata.MetadataRegistry
	// Metadata is the live, request-serving registry — an atomic.Pointer (the Go analogue of
	// Rust's ArcSwap) so a future low-code publish/rollback can swap it while the server keeps
	// running.
	Metadata    *atomic.Pointer[metadata.MetadataRegistry]
	Permissions *permission.PermissionService
	Crud        *crud.CrudService

	// JWTPrivateKeyPEM/JWTPublicKey back MintToken/VerifyToken's static-RSA-keypair path — the
	// only path this core wave wires (the Rust original's metap-jwks dual-dispatch TokenVerifier/
	// TokenSigner opt-in is deferred).
	JWTPrivateKeyPEM string
	JWTPublicKey     any

	// AuthContextEntity: opt-in AUTH_CONTEXT_ENTITY name, "" means unset (the default).
	AuthContextEntity      string
	ContextAttributesCache *control.ContextAttributesCache

	Config *config.Store

	// CookieSecure is the Secure attribute on the session/CSRF cookies — true (the Rust
	// default) unless a local http://localhost dev binary sets it false explicitly.
	CookieSecure bool

	// MetadataResolver is opt-in per-tenant metadata resolution — nil means every tenant shares
	// the single Metadata snapshot above, today's behavior.
	MetadataResolver metadata.MetadataResolver

	// ObjectStore backs /api/{entity}/{id}/attachments* — nil (the default) means every
	// attachment route degrades to a 503, same shape any other optional feature not configured.
	ObjectStore storage.ObjectStore
	// AttachmentTables maps an entity name to its own dedicated attachments table; an entity
	// absent from this map uses the shared default table ("attachments").
	AttachmentTables map[string]string

	// Metrics backs GET /metrics — always present (same "always-on, like /health" posture the
	// Rust original's AppState.metrics_handle has), not an opt-in field.
	Metrics *runtime.MetricsRegistry
}

// NewAppState mirrors the Rust AppState::new. router is built by the caller (same reasoning as
// the Rust original: the composition root needs the same *control.Router for building
// PostgresPolicyStore before this runs too).
func NewAppState(pool *pgxpool.Pool, metadataBase *metadata.MetadataRegistry, md *atomic.Pointer[metadata.MetadataRegistry],
	permissions *permission.PermissionService, jwtPrivateKeyPEM string, jwtPublicKey any, router *control.Router) *AppState {
	crudService := crud.NewCrudService(router, md, permissions)
	return &AppState{
		Pool: pool, Router: router, MetadataBase: metadataBase, Metadata: md, Permissions: permissions,
		Crud: crudService, JWTPrivateKeyPEM: jwtPrivateKeyPEM, JWTPublicKey: jwtPublicKey,
		ContextAttributesCache: control.NewContextAttributesCache(30 * time.Second),
		Config:                 config.Unloaded(pool),
		CookieSecure:           true,
		Metrics:                runtime.NewMetricsRegistry(),
	}
}

// VerifyToken verifies a Bearer/session token against the static RSA keypair — the one place
// AuthContext decodes a token.
func (s *AppState) VerifyToken(token string, leeway time.Duration) (*peripherals.AccessClaims, error) {
	return peripherals.DecodeAccessToken(token, s.JWTPublicKey, leeway)
}

// MintToken is the mint-side counterpart to VerifyToken.
func (s *AppState) MintToken(tenantID, userID uuid.UUID, ttlSeconds uint64) (string, error) {
	return peripherals.MintJWT(s.JWTPrivateKeyPEM, tenantID, userID, ttlSeconds)
}

// MetadataFor is the registry tenantID should see right now.
func (s *AppState) MetadataFor(ctx context.Context, tenantID uuid.UUID) (*metadata.MetadataRegistry, error) {
	if s.MetadataResolver != nil {
		return s.MetadataResolver.Resolve(ctx, tenantID)
	}
	return s.Metadata.Load(), nil
}

// EffectiveConfig is one tenant's effective configuration — cache-first; a lookup failure
// degrades to the fleet-wide values rather than propagating.
func (s *AppState) EffectiveConfig(ctx context.Context, tenantID uuid.UUID) *config.EffectiveConfig {
	if cached, ok := s.Config.CachedTenant(tenantID); ok {
		return s.Config.Effective(cached)
	}
	tx, err := s.Router.Begin(ctx, tenantID)
	if err != nil {
		return s.Config.Effective(nil)
	}
	snapshot, err := s.Config.LoadTenant(ctx, tx, tenantID)
	if err != nil {
		tx.Rollback(ctx) //nolint:errcheck
		return s.Config.Effective(nil)
	}
	tx.Commit(ctx) //nolint:errcheck
	return s.Config.Effective(snapshot)
}
