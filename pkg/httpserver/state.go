// Package httpserver is metap-http: the REST entity CRUD router plus minimal session auth.
// Admin/platform routes (users/policies/cron/dashboards/config/oauth) are deferred to a later wave
// — this package covers only /health, /auth/*, and /api/{entity}*.
package httpserver

import (
	"context"
	"github.com/metap-org/metap/internal/authctx"
	"github.com/metap-org/metap/pkg/auth/captcha"
	"net/http"
	"net/netip"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/metap-org/metap/internal/admin"
	"github.com/metap-org/metap/internal/signin"
	"github.com/metap-org/metap/pkg/audit"
	"github.com/metap-org/metap/pkg/auth/identity"
	"github.com/metap-org/metap/pkg/auth/jwks"
	"github.com/metap-org/metap/pkg/auth/mail"
	"github.com/metap-org/metap/pkg/config"
	"github.com/metap-org/metap/pkg/crud"
	"github.com/metap-org/metap/pkg/httpmw"
	"github.com/metap-org/metap/pkg/metadata"
	"github.com/metap-org/metap/pkg/permission"
	"github.com/metap-org/metap/pkg/storage"
	"github.com/metap-org/metap/pkg/tenancy"
)

// Optional features (TokenSigner/TokenVerifier/JwksKeys for the JWKS trust root, MetadataResolver,
// AttachmentTables, ObjectStore) start unset.
type AppState struct {
	// recordPosts are the POST /api/{entity}/{recordId}/{sub} handlers route modules registered (HandleRecordPost).
	recordPosts map[string]http.HandlerFunc

	Pool *pgxpool.Pool
	// Router is the multi-tenant seam — every tenant-scoped query goes through
	// Router.Begin(ctx, tenantID), never Pool directly.
	Router *tenancy.Router
	// MetadataBase: code-authored entities only, fixed after boot.
	MetadataBase *metadata.MetadataRegistry
	// Metadata is the live, request-serving registry — an atomic.Pointer.
	Metadata    *atomic.Pointer[metadata.MetadataRegistry]
	Permissions *permission.PermissionService
	Crud        *crud.CrudService
	// Records is the record backend the REST entity routes serve (roadmap 116): state.Crud, or a
	// service's decorated one (app.Options.WrapBackend, e.g. create/update/delete guards). nil
	// means Crud.
	Records crud.RecordBackend
	// Actions runs the actions the service's entities declare (EntityDefinition.Actions), served at
	// POST /api/{entity}[/{id}]/actions/{action}; nil when the service declares none.
	Actions crud.ActionInvoker
	// Identity serves user, role, tenant and account administration (pkg/adminapi); set on the
	// token issuer only, nil on a verify-only service.
	Identity *admin.IdentityConfig
	// PublicAppURL is the origin people reach this service at (the SSO callback is PublicAppURL + /auth/sso/callback); "" turns SSO off.
	PublicAppURL string
	// Audit is the tenant's audit trail, for the admin export; nil when none is configured.
	Audit audit.Store

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
	ContextAttributesCache *authctx.ContextAttributesCache
	// RolesCache reuses a member's resolved roles for its TTL (roadmap 120); nil reads them on every
	// request. The session check is never cached. NewAppState leaves it nil; pkg/app sets it from
	// ROLE_CACHE_TTL_SECONDS (default 45).
	RolesCache *authctx.RolesCache

	Config *config.Store
	// Mailer sends invitation and password-reset emails (roadmap 113 P2); nil sends none.
	Mailer *mail.Mailer
	// Captcha, when set, must pass for POST /auth/signup (CAPTCHA_PROVIDER + CAPTCHA_SECRET); nil = off.
	Captcha captcha.Verifier
	// TrustedProxies are the proxies whose X-Forwarded-For is believed when working out the caller's
	// address (httpx.ClientIP): the rate limit and the signup captcha use it. Empty: the TCP peer.
	TrustedProxies []netip.Prefix

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

	Metrics *httpmw.MetricsRegistry
}

func NewAppState(pool *pgxpool.Pool, metadataBase *metadata.MetadataRegistry, md *atomic.Pointer[metadata.MetadataRegistry],
	permissions *permission.PermissionService, jwtPrivateKeyPEM string, jwtPublicKey any, router *tenancy.Router) *AppState {
	crudService := crud.NewCrudService(router, md, permissions)
	return &AppState{
		Pool: pool, Router: router, MetadataBase: metadataBase, Metadata: md, Permissions: permissions,
		Crud: crudService, JWTPrivateKeyPEM: jwtPrivateKeyPEM, JWTPublicKey: jwtPublicKey,
		ContextAttributesCache: authctx.NewContextAttributesCache(contextAttributesCacheTTL),
		Config:                 config.Unloaded(pool),
		CookieSecure:           true,
		Metrics:                httpmw.NewMetricsRegistry(),
	}
}

// ErrNotTokenIssuer: this service verifies tokens but does not issue them (single-issuer JWKS
// deployments) — log in at the issuer instead.
var ErrNotTokenIssuer = signin.ErrNotTokenIssuer

// VerifyToken verifies a Bearer/session token — the one place AuthContext decodes a token.
func (s *AppState) VerifyToken(ctx context.Context, token string, leeway time.Duration) (*jwks.AccessClaims, error) {
	if s.TokenVerifier != nil {
		return jwks.DecodeWithVerifier(ctx, token, s.TokenVerifier, &leeway)
	}
	return jwks.DecodeAccessToken(token, s.JWTPublicKey, leeway)
}

// CanIssueTokens reports whether this service mints tokens (TokenSigner or an RSA private key).
func (s *AppState) CanIssueTokens() bool { return s.TokenSigner != nil || s.JWTPrivateKeyPEM != "" }

// MintToken mints a session token (TokenSigner first, else the static RSA key).
func (s *AppState) MintToken(tenantID, userID uuid.UUID, ttlSeconds uint64) (string, error) {
	switch {
	case s.TokenSigner != nil:
		return jwks.MintWithSigner(s.TokenSigner, tenantID, userID, nil, ttlSeconds)
	case s.JWTPrivateKeyPEM != "":
		return jwks.MintRSAJWT(s.JWTPrivateKeyPEM, tenantID, userID, ttlSeconds)
	default:
		return "", ErrNotTokenIssuer
	}
}

// MintSessionToken mints a login-session token carrying the session id ("sid"); a "" id mints a
// plain session token like MintToken.
func (s *AppState) MintSessionToken(tenantID, userID uuid.UUID, ttlSeconds uint64, sessionID string) (string, error) {
	if sessionID == "" {
		return s.MintToken(tenantID, userID, ttlSeconds)
	}
	switch {
	case s.TokenSigner != nil:
		return jwks.MintSessionWithSigner(s.TokenSigner, tenantID, userID, ttlSeconds, sessionID)
	case s.JWTPrivateKeyPEM != "":
		return jwks.MintRSASessionJWT(s.JWTPrivateKeyPEM, tenantID, userID, ttlSeconds, sessionID)
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
		return jwks.MintRSAOAuthAccessToken(s.JWTPrivateKeyPEM, tenantID, userID, ttlSeconds, scope, clientID)
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

// PasswordPolicy is each tenant's minimum password length (auth.passwordMinLength).
func (s *AppState) PasswordPolicy() identity.PasswordPolicy {
	return func(ctx context.Context, tenantID uuid.UUID) int {
		if s.Config == nil {
			return identity.MinPasswordLength
		}
		return int(s.EffectiveConfig(ctx, tenantID).GetU64(config.AuthPasswordMinLength))
	}
}

// APIKeyMaxDays is each tenant's cap on an API key's lifetime (auth.apiKeyMaxLifetimeDays), 0 = none.
func (s *AppState) APIKeyMaxDays(ctx context.Context, tenantID uuid.UUID) int {
	if s.Config == nil {
		return 0
	}
	return int(s.EffectiveConfig(ctx, tenantID).GetU64(config.AuthAPIKeyMaxLifetimeDays))
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

// contextAttributesCacheTTL is how long a user's resolved context attributes are reused before the
// next request re-reads them (an explicit invalidation also clears the entry).
const contextAttributesCacheTTL = 30 * time.Second

// RecordBackend is the backend entity routes read and write through: Records when set, else Crud.
func (s *AppState) RecordBackend() crud.RecordBackend {
	if s.Records != nil {
		return s.Records
	}
	return s.Crud
}

// Signin is the sign-in service on this state's database, configuration and signing key.
func (s *AppState) Signin() *signin.Service {
	return signin.New(signin.Deps{
		Pool: s.Pool, Effective: s.EffectiveConfig, MintSession: s.MintSessionToken, PasswordPolicy: s.PasswordPolicy(),
		Platform: s.platformConfig, Mailer: s.Mailer, Captcha: s.Captcha,
		PublicURL: s.PublicAppURL, Router: s.Router,
	})
}

func (s *AppState) platformConfig() *config.Snapshot {
	if s.Config == nil {
		return nil
	}
	return s.Config.Current()
}
