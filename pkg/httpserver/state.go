// Package httpserver is metap-http: the REST entity CRUD router plus minimal session auth.
// Admin/platform routes (users/policies/cron/dashboards/config/oauth) are deferred to a later wave
// — this package covers only /health, /auth/*, and /api/{entity}*.
package httpserver

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/metap-org/metap/pkg/config"
	"github.com/metap-org/metap/pkg/control"
	"github.com/metap-org/metap/pkg/crud"
	"github.com/metap-org/metap/pkg/jwks"
	"github.com/metap-org/metap/pkg/metadata"
	"github.com/metap-org/metap/pkg/peripherals"
	"github.com/metap-org/metap/pkg/permission"
	"github.com/metap-org/metap/pkg/runtime"
	"github.com/metap-org/metap/pkg/storage"
)

// Optional features (TokenSigner/TokenVerifier/JwksKeys for the JWKS trust root, MetadataResolver,
// AttachmentTables, ObjectStore) start unset.
type AppState struct {
	Pool *pgxpool.Pool
	// Router is the multi-tenant seam — every tenant-scoped query goes through
	// Router.Begin(ctx, tenantID), never Pool directly.
	Router *control.Router
	// MetadataBase: code-authored entities only, fixed after boot.
	MetadataBase *metadata.MetadataRegistry
	// Metadata is the live, request-serving registry — an atomic.Pointer.
	Metadata    *atomic.Pointer[metadata.MetadataRegistry]
	Permissions *permission.PermissionService
	Crud        *crud.CrudService

	// JWTPrivateKeyPEM/JWTPublicKey: the legacy static-RSA keypair. Used only when
	// TokenSigner/TokenVerifier below are nil. JWTPrivateKeyPEM "" means this service never mints
	// RS256 tokens.
	JWTPrivateKeyPEM string
	JWTPublicKey     any

	// TokenSigner, when set, mints every token (login, refresh, OAuth2) — set only on the one
	// service that is the token issuer. A service with neither TokenSigner nor JWTPrivateKeyPEM
	// is not an issuer: MintToken returns ErrNotTokenIssuer and /auth/login answers 501.
	TokenSigner *jwks.TokenSigner
	// TokenVerifier, when set, verifies every inbound token (typically jwks.VerifierAuto: EdDSA
	// via the issuer's JWKS, optionally still accepting legacy RS256 during a cutover).
	TokenVerifier *jwks.TokenVerifier
	// JwksKeys, when set, makes this service publish GET /.well-known/jwks.json (the issuer).
	JwksKeys *jwks.SharedKeyStore

	// AuthContextEntity: opt-in AUTH_CONTEXT_ENTITY name, "" means unset (the default).
	AuthContextEntity      string
	ContextAttributesCache *control.ContextAttributesCache

	Config *config.Store

	// CookieSecure is the Secure attribute on the session/CSRF cookies — true.
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

	Metrics *runtime.MetricsRegistry
}

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

// ErrNotTokenIssuer: this service verifies tokens but does not issue them (single-issuer JWKS
// deployments) — log in at the issuer instead.
var ErrNotTokenIssuer = errors.New("this service does not issue tokens; log in at the token issuer")

// VerifyToken verifies a Bearer/session token — the one place AuthContext decodes a token.
func (s *AppState) VerifyToken(ctx context.Context, token string, leeway time.Duration) (*peripherals.AccessClaims, error) {
	if s.TokenVerifier != nil {
		return jwks.DecodeWithVerifier(ctx, token, s.TokenVerifier, &leeway)
	}
	return peripherals.DecodeAccessToken(token, s.JWTPublicKey, leeway)
}

// CanIssueTokens reports whether this service mints tokens (TokenSigner or an RSA private key).
func (s *AppState) CanIssueTokens() bool { return s.TokenSigner != nil || s.JWTPrivateKeyPEM != "" }

// MintToken mints a session token (TokenSigner first, else the static RSA key).
func (s *AppState) MintToken(tenantID, userID uuid.UUID, ttlSeconds uint64) (string, error) {
	switch {
	case s.TokenSigner != nil:
		return jwks.MintWithSigner(s.TokenSigner, tenantID, userID, nil, ttlSeconds)
	case s.JWTPrivateKeyPEM != "":
		return peripherals.MintJWT(s.JWTPrivateKeyPEM, tenantID, userID, ttlSeconds)
	default:
		return "", ErrNotTokenIssuer
	}
}

// MintOAuthToken mints an access token carrying the OAuth2 grant's scope/clientId claims.
func (s *AppState) MintOAuthToken(tenantID, userID uuid.UUID, ttlSeconds uint64, scope, clientID string) (string, error) {
	switch {
	case s.TokenSigner != nil:
		return jwks.MintOAuthTokenWithSigner(s.TokenSigner, tenantID, userID, ttlSeconds, scope, clientID)
	case s.JWTPrivateKeyPEM != "":
		return peripherals.MintOAuthAccessToken(s.JWTPrivateKeyPEM, tenantID, userID, ttlSeconds, scope, clientID)
	default:
		return "", ErrNotTokenIssuer
	}
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
