// Package app is the boot sequence every metadata-driven service built on metap repeats:
// config -> Postgres -> tenant router -> permissions -> JWT keys -> metadata registry ->
// reconcile the service's own dedicated tables -> metadata drift check -> CRUD (+audit) -> optional gRPC
// -> HTTP. A service's main shrinks to its entity list, its port numbers, and its own routes:
// import _ "example.com/scanning/entities"
//
//	func main() {
//		app.Main(app.Options{
//			Name:     "scanning-service",
//			// entities come from the blank-imported entities package's init() (metadata.SubmitEntity)
//			Routes:   scanningservice.MountRoutes,
//		})
//	}
package app

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"github.com/metap-org/metap/pkg/captcha"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/metap-org/metap/internal/admin"
	"github.com/metap-org/metap/pkg/accountmail"
	"github.com/metap-org/metap/pkg/audit"
	"github.com/metap-org/metap/pkg/control"
	"github.com/metap-org/metap/pkg/crud"
	"github.com/metap-org/metap/pkg/httpserver"
	"github.com/metap-org/metap/pkg/httpserver/attachmentroutes"
	"github.com/metap-org/metap/pkg/httpserver/cronroutes"
	"github.com/metap-org/metap/pkg/httpserver/dashboardroutes"
	"github.com/metap-org/metap/pkg/httpserver/oauth2routes"
	"github.com/metap-org/metap/pkg/infra"
	"github.com/metap-org/metap/pkg/jobs/scheduler"
	"github.com/metap-org/metap/pkg/kit/env"
	"github.com/metap-org/metap/pkg/kit/httpx"
	"github.com/metap-org/metap/pkg/kit/shutdown"
	"github.com/metap-org/metap/pkg/kit/standard"
	"github.com/metap-org/metap/pkg/metadata"
	"github.com/metap-org/metap/pkg/permission"
	"github.com/metap-org/metap/pkg/reconciler"
	"github.com/metap-org/metap/pkg/secrets"
)

// Options describes one service.
type Options struct {
	// Name labels log lines, e.g. "zones-service".
	Name string

	// Entities is normally left empty: every entity the service's packages declare with
	// metadata.SubmitEntity (from an init(), next to the definition) is registered and reconciled
	// at boot, referenced entities first. Set it only to reconcile a different set or order
	// explicitly — those are then reconciled instead of the submitted ones. Shared
	// Schema-strategy tables are backfilled across every tenant (reconciler.ScopeAllTenants).
	Entities []metadata.EntityDefinition

	// WrapBackend, if set, decorates the CRUD backend the REST API serves (/api/{entity}) — e.g.
	// to add service-specific create/update/delete guards. Defaults to state.Crud as-is.
	WrapBackend func(state *httpserver.AppState) (crud.RecordBackend, error)

	// ExternalEntities are entities this service serves whose records live outside its database
	// (a read model over another system): registered and served like the rest (metadata, REST),
	// never reconciled, every call going to the entity's own Backend.
	ExternalEntities []ExternalEntity

	// Actions, if set, registers the handlers for the actions this service's entities declare
	// (EntityDefinition.Actions); they are served over REST (POST /api/{entity}[/{id}]/actions/{action}).
	// Handlers read and write through state.Crud (or the wrapped backend) with the caller's own
	// RequestContext, so permissions bound them exactly like plain CRUD.
	Actions func(state *httpserver.AppState, registry *crud.ActionRegistry) error

	// Routes mounts the service's own custom HTTP routes next to metap's generic ones.
	Routes httpserver.Mount

	// Configure, if set, runs after AppState is built and before anything is served — the
	// escape hatch for per-service AppState tweaks.
	Configure func(state *httpserver.AppState) error

	// Modules are the optional route groups to serve next to the core ones. nil means
	// DefaultModules (everything — the behavior before route groups were split out); pass an
	// empty, non-nil slice to serve none.
	Modules []httpserver.Mount
}

// DefaultModules is every optional route group: the OAuth2 authorization server, cron job
// admin, dashboards, and record attachments (which answers 503 until state.ObjectStore is set).
func DefaultModules() []httpserver.Mount {
	return []httpserver.Mount{oauth2routes.Mount, cronroutes.Mount, dashboardroutes.Mount, attachmentroutes.Mount}
}

// Main runs the service and exits the process with status 1 on any error.
func Main(opts Options) {
	if err := Run(context.Background(), opts); err != nil {
		slog.Error(opts.Name+" exited with an error", standard.LogError, err)
		os.Exit(1)
	}
}

// ExternalEntity is one Options.ExternalEntities entry.
type ExternalEntity struct {
	Definition metadata.EntityDefinition
	Backend    func(state *httpserver.AppState) (crud.RecordBackend, error)
}

// Run boots the service and serves until ctx is cancelled, SIGINT/SIGTERM, or a listener failure.
func Run(ctx context.Context, opts Options) error {
	infra.InitTracing()
	config, err := infra.LoadConfig()
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	pool, err := infra.Connect(ctx, config.DatabaseURL)
	if err != nil {
		return fmt.Errorf("connecting to postgres: %w", err)
	}
	defer pool.Close()

	secretManager, err := secrets.NewManager(ctx, secrets.ConfigFromAppConfig(config), pool)
	if err != nil {
		return fmt.Errorf("building secret store: %w", err)
	}
	router := control.NewRouter(pool, control.NewRegistryCache(control.NewPostgresTenantRegistry(pool)), secretManager)
	permissions := permission.NewPermissionService(control.NewPostgresPolicyStore(router))

	tokens, err := BuildTokenAuth(ctx, config)
	if err != nil {
		return fmt.Errorf("configuring token auth: %w", err)
	}
	slog.Info("token auth", "mode", tokens.Mode())

	registry := metadata.NewMetadataRegistry()
	if err := registry.RegisterAllSubmitted(); err != nil {
		return fmt.Errorf("registering entities: %w", err)
	}
	if err := registry.ValidateReferences(); err != nil {
		return fmt.Errorf("validating entity references: %w", err)
	}
	toReconcile := opts.Entities
	if len(toReconcile) == 0 {
		toReconcile = registry.Definitions()
	}
	for _, entity := range toReconcile {
		outcome, err := reconciler.ReconcileWithScope(ctx, pool, control.PlatformTenantID, entity, nil, reconciler.ScopeAllTenants)
		if err != nil {
			return fmt.Errorf("reconciling %s: %w", entity.Name, err)
		}
		slog.Info("reconciled dedicated table", "entity", entity.Name, "table", outcome.Table, "ops_applied", outcome.OpsApplied)
	}

	// Indexes for dedicated tables are built by the reconcile above; peripherals' old
	// records-table index reconciler was removed (that table was dropped in migration 0033).
	metadata.CheckDrift(ctx, pool, registry.ListEntities())
	for _, ext := range opts.ExternalEntities {
		if err := registry.Register(ext.Definition); err != nil {
			return fmt.Errorf("registering external entity %s: %w", ext.Definition.Name, err)
		}
	}

	// Opt-in first-run bootstrap for a single-service product. A product with several services
	// should run its one-shot bootstrap process (BootstrapMain) instead: each service only knows
	// its own entities, and two services applying at once could both create the same policy.
	if env.Bool("BOOTSTRAP_ON_BOOT") {
		decl, err := declaration(nil)
		if err != nil {
			return fmt.Errorf("bootstrap on boot: %w", err)
		}
		tenants, err := bootstrapTargets(ctx, pool, env.String("BOOTSTRAP_TENANT_IDS", ""))
		if err != nil {
			return fmt.Errorf("bootstrap on boot: %w", err)
		}
		if err := ApplyBootstrap(ctx, registry, permissions, router, tenants, decl, config.NodeEnv == infra.NodeEnvProduction, opts.Name+" bootstrap"); err != nil {
			return fmt.Errorf("bootstrap on boot: %w", err)
		}
	}

	var mdPtr atomic.Pointer[metadata.MetadataRegistry]
	mdPtr.Store(registry)

	var publicKey any
	if tokens.PublicKey != nil {
		publicKey = tokens.PublicKey
	}
	state := httpserver.NewAppState(pool, registry, &mdPtr, permissions, tokens.PrivateKeyPEM, publicKey, router)
	state.TokenSigner, state.TokenVerifier, state.JwksKeys = tokens.Signer, tokens.Verifier, tokens.Keys
	auditStore := audit.NewPostgresStore(state.Pool)
	state.Audit = auditStore
	state.PublicAppURL = config.PublicAppURL
	state.Crud = crud.WithAudit(state.Router, state.Metadata, state.Permissions, auditStore)
	state.CookieSecure = false
	state.Mailer = accountmail.New(accountmail.Config{
		Host: config.SMTPHost, Port: config.SMTPPort, User: config.SMTPUser, Password: config.SMTPPassword,
		From: config.SMTPFrom, PublicURL: config.PublicAppURL,
	})
	if state.Captcha, err = captcha.Build(env.String("CAPTCHA_PROVIDER", ""), env.String("CAPTCHA_SECRET", "")); err != nil {
		return err
	}
	if state.CanIssueTokens() && !state.Mailer.Configured() {
		slog.Warn("SMTP_HOST/SMTP_FROM not set: invitation and password-reset emails are not sent " +
			"(an invitation's link is returned to the inviting admin instead)")
	}
	if opts.Configure != nil {
		if err := opts.Configure(state); err != nil {
			return fmt.Errorf("configuring app state: %w", err)
		}
	}

	// The record backend and the actions REST serves: the service's decorated backend when it
	// has one, so its guards hold on every route.
	if opts.WrapBackend != nil {
		if state.Records, err = opts.WrapBackend(state); err != nil {
			return fmt.Errorf("building record backend: %w", err)
		}
	}
	if len(opts.ExternalEntities) > 0 {
		external := make(map[string]crud.RecordBackend, len(opts.ExternalEntities))
		for _, ext := range opts.ExternalEntities {
			if external[ext.Definition.Name], err = ext.Backend(state); err != nil {
				return fmt.Errorf("building backend for %s: %w", ext.Definition.Name, err)
			}
		}
		state.Records = crud.RouteBackends(state.RecordBackend(), external)
	}
	if opts.Actions != nil {
		reg := crud.NewActionRegistry()
		if err := opts.Actions(state, reg); err != nil {
			return fmt.Errorf("registering actions: %w", err)
		}
		for _, def := range registry.Definitions() {
			for _, a := range def.Actions {
				if !reg.Has(def.Name, a.Name) {
					return fmt.Errorf("entity %s declares action %s but no handler is registered", def.Name, a.Name)
				}
			}
		}
		state.Actions = reg
	}

	modules := opts.Modules
	if modules == nil {
		modules = DefaultModules()
	}
	trusted, err := httpx.ParseTrustedProxies(config.TrustedProxies)
	if err != nil {
		return fmt.Errorf("TRUSTED_PROXIES: %w", err)
	}
	state.TrustedProxies = trusted
	state.Identity = identityConfig(state, auditStore, secretManager)
	handler := httpserver.BuildRouter(state, config.CORSOrigins, append(modules, opts.Routes)...)
	addr := fmt.Sprintf("%s:%d", config.Host, config.Port)
	slog.Info(opts.Name+" listening", "addr", addr)
	serveCtx, stop := shutdown.Context(ctx)
	defer stop()
	return httpx.ServeContext(serveCtx, &http.Server{Addr: addr, Handler: handler}, httpx.DefaultShutdownTimeout)
}

// identityConfig serves user administration on the token issuer only (it owns identities); a
// verify-only service returns nil.
func identityConfig(state *httpserver.AppState, auditStore audit.Store, manager *secrets.Manager) *admin.IdentityConfig {
	if !state.CanIssueTokens() {
		return nil
	}
	return &admin.IdentityConfig{Shared: state.Pool, Router: state.Router, Audit: auditStore,
		Mailer: state.Mailer, PasswordPolicy: state.PasswordPolicy(), APIKeyMaxDays: state.APIKeyMaxDays, PublicURL: state.PublicAppURL,
		Credentials: credentialAdapter{manager}, HTTPClient: metadataHTTPClient()}
}

// metadataHTTPClient fetches a SAML provider's metadata from a URL a tenant admin typed in. It
// refuses to connect to loopback, private and link-local addresses (checked on the address actually
// dialled, so a name that resolves inward is caught too) unless SSO_METADATA_ALLOW_PRIVATE_HOSTS is
// set, which a development stack with its provider on the same host needs.
func metadataHTTPClient() *http.Client {
	allow, _ := env.Lookup("SSO_METADATA_ALLOW_PRIVATE_HOSTS")
	if allow == "true" {
		return &http.Client{Timeout: 15 * time.Second}
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second, Control: func(_, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return err
		}
		if ip := net.ParseIP(host); ip == nil || scheduler.IsBlockedIP(ip) {
			return fmt.Errorf("metadata fetches may not connect to %s", host)
		}
		return nil
	}}
	return &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{DialContext: dialer.DialContext}}
}

// ReadPublicKey loads a PEM public key: PKIX ("PUBLIC KEY", any algorithm) first, falling back to
// PKCS#1 RSA ("RSA PUBLIC KEY").
func ReadPublicKey(path string) (any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, jwt.ErrKeyMustBePEMEncoded
	}
	if pub, err := x509.ParsePKIXPublicKey(block.Bytes); err == nil {
		return pub, nil
	}
	return jwt.ParseRSAPublicKeyFromPEM(data)
}

// credentialAdapter presents the secrets.Manager to adminapi, which cannot import the package
// (it links the cloud SDKs).
type credentialAdapter struct{ m *secrets.Manager }

func (a credentialAdapter) Capabilities() admin.CredentialCapabilities {
	c := a.m.Capabilities()
	return admin.CredentialCapabilities{Database: c.Database, External: c.External, ExternalKind: c.Kind}
}

func (a credentialAdapter) Put(ctx context.Context, ref, source, value string) error {
	return a.m.Put(ctx, ref, source, value)
}

func (a credentialAdapter) Remove(ctx context.Context, ref string) error { return a.m.Remove(ctx, ref) }

func (a credentialAdapter) Get(ctx context.Context, ref string) (string, error) {
	return a.m.GetSecret(ctx, ref)
}
