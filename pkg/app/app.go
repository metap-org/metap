// Package app is the boot sequence every metadata-driven service built on metap repeats:
// config -> Postgres -> tenant router -> permissions -> JWT keys -> metadata registry ->
// check the schema (never change it; `<service> migrate` does) -> metadata drift check -> CRUD (+audit) -> HTTP.
// A service's main shrinks to its entity list, its port numbers, and its own routes:
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
	"errors"
	"fmt"
	"github.com/metap-org/metap/pkg/auth/captcha"
	"github.com/metap-org/metap/pkg/permission/policypg"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/metap-org/metap/internal/admin"
	"github.com/metap-org/metap/internal/authctx"
	"github.com/metap-org/metap/internal/httpapi"
	"github.com/metap-org/metap/internal/httpapi/attachmentroutes"
	"github.com/metap-org/metap/internal/httpapi/cronroutes"
	"github.com/metap-org/metap/internal/httpapi/dashboardroutes"
	"github.com/metap-org/metap/internal/httpapi/oauth2routes"
	"github.com/metap-org/metap/pkg/action"
	"github.com/metap-org/metap/pkg/audit"
	"github.com/metap-org/metap/pkg/auth/mail"
	"github.com/metap-org/metap/pkg/config"
	"github.com/metap-org/metap/pkg/crud"
	"github.com/metap-org/metap/pkg/db"
	"github.com/metap-org/metap/pkg/jobs/scheduler"
	"github.com/metap-org/metap/pkg/kit/env"
	"github.com/metap-org/metap/pkg/kit/httpx"
	"github.com/metap-org/metap/pkg/kit/httpx/router"
	"github.com/metap-org/metap/pkg/kit/shutdown"
	"github.com/metap-org/metap/pkg/kit/standard"
	"github.com/metap-org/metap/pkg/metadata"
	"github.com/metap-org/metap/pkg/permission"
	"github.com/metap-org/metap/pkg/secrets/backend"
	"github.com/metap-org/metap/pkg/tenancy"
)

// Options describes one service.
type Options struct {
	// Name labels log lines, e.g. "zones-service".
	Name string

	// Entities is normally left empty: every entity the service's packages declare with
	// metadata.SubmitEntity (from an init(), next to the definition) is registered, and its table
	// must exist before the service starts (see Migrations). Set it only to name a different set of
	// stored entities explicitly.
	Entities []metadata.EntityDefinition

	// Migrations are the SQL files that create and change this service's entity tables, written by
	// `<service> migrate generate`, committed, and embedded in the binary. Booting never changes
	// the schema: a service whose database lacks anything does not start (docs/migrations.md).
	// MigrationsDir is where `generate` and `new` write them, relative to where they are run.
	Migrations    fs.FS
	MigrationsDir string

	// WrapBackend, if set, decorates the CRUD backend the REST API serves (/api/{entity}) — e.g.
	// to add service-specific create/update/delete guards. Defaults to state.Crud as-is.
	WrapBackend func(state *httpapi.AppState) (crud.RecordBackend, error)

	// ExternalEntities are entities this service serves whose records live outside its database
	// (a read model over another system): registered and served like the rest (metadata, REST),
	// never reconciled, every call going to the entity's own Backend.
	ExternalEntities []ExternalEntity

	// Actions, if set, registers the handlers for the actions this service's entities declare
	// (EntityDefinition.Actions); they are served over REST (POST /api/{entity}[/{id}]/actions/{action}).
	// Handlers read and write through state.Crud (or the wrapped backend) with the caller's own
	// RequestContext, so permissions bound them exactly like plain CRUD.
	Actions func(state *httpapi.AppState, registry *crud.ActionRegistry) error

	// Routes mounts the service's own custom HTTP routes next to metap's generic ones.
	Routes httpapi.Mount

	// Configure, if set, runs after AppState is built and before anything is served — the
	// escape hatch for per-service AppState tweaks.
	Configure func(state *httpapi.AppState) error

	// Modules are the optional route groups to serve next to the core ones. nil means
	// DefaultModules (everything — the behavior before route groups were split out); pass an
	// empty, non-nil slice to serve none.
	Modules []httpapi.Mount
}

// DefaultModules is every optional route group: the OAuth2 authorization server, cron job
// admin, dashboards, and record attachments (which answers 503 until state.ObjectStore is set).
func DefaultModules() []httpapi.Mount {
	return []httpapi.Mount{oauth2routes.Mount, cronroutes.Mount, dashboardroutes.Mount, attachmentroutes.Mount}
}

// Main runs the service and exits the process with status 1 on any error.
// `<service> migrate ...` runs the migration command instead of the service.
func Main(opts Options) {
	if len(os.Args) > 1 && os.Args[1] == "migrate" {
		if err := MigrateCommand(context.Background(), opts, os.Args[2:], os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "migrate:", err)
			os.Exit(1)
		}
		return
	}
	if err := Run(context.Background(), opts); err != nil {
		// The list of what is missing, and what to run, as lines a person can read.
		if errors.Is(err, ErrSchemaNotReady) {
			fmt.Fprintln(os.Stderr, err)
			err = ErrSchemaNotReady
		}
		slog.Error(opts.Name+" exited with an error", standard.LogError, err)
		os.Exit(1)
	}
}

// ExternalEntity is one Options.ExternalEntities entry.
type ExternalEntity struct {
	Definition metadata.EntityDefinition
	Backend    func(state *httpapi.AppState) (crud.RecordBackend, error)
}

// Run boots the service and serves until ctx is cancelled, SIGINT/SIGTERM, or a listener failure.
func Run(ctx context.Context, opts Options) error {
	config.InitLogging()
	appCfg, err := config.LoadApp()
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	pool, err := db.Connect(ctx, appCfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("connecting to postgres: %w", err)
	}
	defer pool.Close()

	secretManager, err := backend.NewManager(ctx, backend.ConfigFromAppConfig(appCfg), pool)
	if err != nil {
		return fmt.Errorf("building secret store: %w", err)
	}
	router := tenancy.NewRouter(pool, tenancy.NewRegistryCache(tenancy.NewPostgresTenantRegistry(pool)), secretManager)
	policies := policypg.NewPostgresPolicyStore(router)
	// The entity-level checks read a tenant's policies from memory for POLICY_CACHE_TTL_SECONDS
	// (default 45, 0 turns it off); a policy changed through this process applies at once, any
	// other change within the TTL (roadmap 120).
	policyStore := permission.NewMatrixStore(policies, policies.LoadContextPolicies, env.Seconds("POLICY_CACHE_TTL_SECONDS", 45*time.Second))
	permissions := permission.NewPermissionService(policyStore)

	tokens, err := BuildTokenAuth(ctx, appCfg)
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
	// The schema is only read here. Changing it is `<service> migrate`, run by a person or a
	// deploy step, never by a booting service.
	if err := requireSchema(ctx, pool, opts, storedEntities(opts, registry)); err != nil {
		return err
	}
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
		if err := ApplyBootstrap(ctx, registry, permissions, router, tenants, decl, appCfg.NodeEnv == config.NodeEnvProduction, opts.Name+" bootstrap"); err != nil {
			return fmt.Errorf("bootstrap on boot: %w", err)
		}
	}

	var mdPtr atomic.Pointer[metadata.MetadataRegistry]
	mdPtr.Store(registry)

	var publicKey any
	if tokens.PublicKey != nil {
		publicKey = tokens.PublicKey
	}
	state := httpapi.NewAppState(pool, registry, &mdPtr, permissions, tokens.PrivateKeyPEM, publicKey, router)
	state.TokenSigner, state.TokenVerifier, state.JwksKeys = tokens.Signer, tokens.Verifier, tokens.Keys
	// A role change or revocation applies within this TTL (roadmap 120); a session revocation is
	// checked on every request regardless. 0 turns the cache off.
	state.RolesCache = authctx.NewRolesCache(env.Seconds("ROLE_CACHE_TTL_SECONDS", 45*time.Second))
	defer authctx.SubscribeChanges(state.RolesCache, policyStore)()
	auditStore := audit.NewPostgresStore(state.Pool)
	state.Audit = auditStore
	startAuditDrain(ctx, pool, router, auditStore)
	state.PublicAppURL = appCfg.PublicAppURL
	state.Crud = crud.WithAudit(state.Router, state.Metadata, state.Permissions, auditStore)
	state.CookieSecure = appCfg.CookieSecure
	if !state.CookieSecure {
		slog.Warn("COOKIE_SECURE=false: session cookies are sent without the Secure attribute, only for development over plain http")
	}
	state.Mailer = mail.New(mail.Config{
		Host: appCfg.SMTPHost, Port: appCfg.SMTPPort, User: appCfg.SMTPUser, Password: appCfg.SMTPPassword,
		From: appCfg.SMTPFrom, PublicURL: appCfg.PublicAppURL,
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
	trusted, err := httpx.ParseTrustedProxies(appCfg.TrustedProxies)
	if err != nil {
		return fmt.Errorf("TRUSTED_PROXIES: %w", err)
	}
	state.TrustedProxies = trusted
	state.Identity = identityConfig(state, auditStore, secretManager)
	handler, err := buildHandler(state, appCfg.CORSOrigins, append(modules, opts.Routes))
	if err != nil {
		return err
	}
	addr := fmt.Sprintf("%s:%d", appCfg.Host, appCfg.Port)
	slog.Info(opts.Name+" listening", "addr", addr)
	serveCtx, stop := shutdown.Context(ctx)
	defer stop()
	return httpx.ServeContext(serveCtx, &http.Server{Addr: addr, Handler: handler}, httpx.DefaultShutdownTimeout)
}

// buildHandler is the router the service serves. It refuses one with a route outside the access
// groups: such a route answers anyone, and a service that has no test of its own for it would
// otherwise ship it.
func buildHandler(state *httpapi.AppState, corsOrigins []string, mounts []httpapi.Mount) (http.Handler, error) {
	handler := httpapi.BuildRouter(state, corsOrigins, mounts...)
	r, ok := handler.(*router.Router)
	if !ok {
		return nil, fmt.Errorf("checking route access: BuildRouter returned %T, not a router", handler)
	}
	if bad := httpapi.RoutesWithoutOneAccessRule(r); len(bad) > 0 {
		return nil, fmt.Errorf("routes outside the access groups (register each through one of web.Public, "+
			"web.Authenticated, web.Admin, web.PlatformAdmin, web.SelfAuth): %s", strings.Join(bad, "; "))
	}
	return r, nil
}

// identityConfig serves user administration on the token issuer only (it owns identities); a
// verify-only service returns nil.
func identityConfig(state *httpapi.AppState, auditStore audit.Store, manager *backend.Manager) *admin.IdentityConfig {
	if !state.CanIssueTokens() {
		return nil
	}
	return &admin.IdentityConfig{Shared: state.Pool, Router: state.Router, Audit: auditStore, Sink: action.NewSink(state.Router),
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

// credentialAdapter presents the backend.Manager to adminapi, which cannot import the package
// (it links the cloud SDKs).
type credentialAdapter struct{ m *backend.Manager }

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
