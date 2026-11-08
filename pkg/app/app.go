// Package app is the boot sequence every metadata-driven service built on metap repeats:
// config -> Postgres -> tenant router -> permissions -> JWT keys -> metadata registry ->
// reconcile the service's own dedicated tables -> metadata drift check -> CRUD (+audit) -> optional gRPC
// -> HTTP. A service's main shrinks to its entity list, its port numbers, and its own routes:
//
//	func main() {
//		app.Main(app.Options{
//			Name:     "scanning-service",
//			Entities: []metadata.EntityDefinition{entities.ScanJobEntity(), entities.ScanFindingEntity()},
//			GRPCPort: 3011,
//			Routes:   scanningservice.MountRoutes,
//		})
//	}
//
// It is the Go counterpart of the Rust-era metap-app builder (skipped in the Go port until a
// downstream service needed it — ../waap's three management-plane services were hand-copying ~150
// identical lines each).
package app

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sync/atomic"

	"github.com/golang-jwt/jwt/v5"

	"github.com/metap-org/metap/pkg/audit"
	"github.com/metap-org/metap/pkg/control"
	"github.com/metap-org/metap/pkg/crud"
	"github.com/metap-org/metap/pkg/grpcapi"
	"github.com/metap-org/metap/pkg/httpserver"
	"github.com/metap-org/metap/pkg/httpserver/attachmentroutes"
	"github.com/metap-org/metap/pkg/httpserver/cronroutes"
	"github.com/metap-org/metap/pkg/httpserver/dashboardroutes"
	"github.com/metap-org/metap/pkg/httpserver/oauth2routes"
	"github.com/metap-org/metap/pkg/infra"
	"github.com/metap-org/metap/pkg/kit/httpx"
	"github.com/metap-org/metap/pkg/kit/shutdown"
	"github.com/metap-org/metap/pkg/metadata"
	"github.com/metap-org/metap/pkg/peripherals"
	"github.com/metap-org/metap/pkg/permission"
	"github.com/metap-org/metap/pkg/reconciler"
	"github.com/metap-org/metap/pkg/secrets"
)

// Options describes one service.
type Options struct {
	// Name labels log lines, e.g. "zones-service".
	Name string

	// Entities are this service's own dedicated-table entities, reconciled at boot in this order
	// (put a Reference target before the entities that point at it). Shared Schema-strategy
	// tables are backfilled across every tenant (reconciler.ScopeAllTenants).
	Entities []metadata.EntityDefinition

	// GRPCPort, when non-zero, serves the generic CRUD gRPC API on config.Host:GRPCPort (if the
	// service's config enables gRPC — see grpcapi.OptionalServe).
	GRPCPort int

	// WrapBackend, if set, decorates the CRUD backend the gRPC API serves — e.g. to add
	// service-specific create/update/delete guards. Defaults to state.Crud as-is.
	WrapBackend func(state *httpserver.AppState) (crud.RecordBackend, error)

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
		slog.Error(opts.Name+" exited with an error", "error", err)
		os.Exit(1)
	}
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

	secretStore, err := secrets.Build(ctx, secrets.ConfigFromAppConfig(config))
	if err != nil {
		return fmt.Errorf("building secret store: %w", err)
	}
	router := control.NewRouter(pool, control.NewRegistryCache(control.NewPostgresTenantRegistry(pool)), secretStore)
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
	for _, entity := range opts.Entities {
		outcome, err := reconciler.ReconcileWithScope(ctx, pool, control.PlatformTenantID, entity, nil, reconciler.ScopeAllTenants)
		if err != nil {
			return fmt.Errorf("reconciling %s: %w", entity.Name, err)
		}
		slog.Info("reconciled dedicated table", "entity", entity.Name, "table", outcome.Table, "ops_applied", outcome.OpsApplied)
	}

	// Indexes for dedicated tables are built by the reconcile above; peripherals' old
	// records-table index reconciler was removed (that table was dropped in migration 0033).
	peripherals.Check(ctx, pool, registry.ListEntities())

	var mdPtr atomic.Pointer[metadata.MetadataRegistry]
	mdPtr.Store(registry)

	var publicKey any
	if tokens.PublicKey != nil {
		publicKey = tokens.PublicKey
	}
	state := httpserver.NewAppState(pool, registry, &mdPtr, permissions, tokens.PrivateKeyPEM, publicKey, router)
	state.TokenSigner, state.TokenVerifier, state.JwksKeys = tokens.Signer, tokens.Verifier, tokens.Keys
	state.Crud = crud.WithAudit(state.Router, state.Metadata, state.Permissions, audit.NewPostgresStore(state.Pool))
	state.CookieSecure = false
	if opts.Configure != nil {
		if err := opts.Configure(state); err != nil {
			return fmt.Errorf("configuring app state: %w", err)
		}
	}

	if opts.GRPCPort != 0 {
		var backend crud.RecordBackend = state.Crud
		if opts.WrapBackend != nil {
			if backend, err = opts.WrapBackend(state); err != nil {
				return fmt.Errorf("building gRPC backend: %w", err)
			}
		}
		// gRPC verifies exactly like HTTP: the JWKS verifier when configured, else the static key.
		grpcServer, err := grpcapi.OptionalServe(config.Host, opts.GRPCPort, grpcapi.OptionalServeConfig{
			Backend: backend, Router: state.Router, JWTPublicKey: tokens.PublicKey, TokenVerifierOverride: tokens.Verifier,
			AuthContextEntity: state.AuthContextEntity, Metadata: state.Metadata, ContextAttributesCache: state.ContextAttributesCache,
		})
		if err != nil {
			return fmt.Errorf("starting gRPC server: %w", err)
		}
		if grpcServer != nil {
			defer grpcServer.GracefulStop()
		}
	}

	modules := opts.Modules
	if modules == nil {
		modules = DefaultModules()
	}
	handler := httpserver.BuildRouter(state, config.CORSOrigins, append(modules, opts.Routes)...)
	addr := fmt.Sprintf("%s:%d", config.Host, config.Port)
	slog.Info(opts.Name+" listening", "addr", addr)
	serveCtx, stop := shutdown.Context(ctx)
	defer stop()
	return httpx.ServeContext(serveCtx, &http.Server{Addr: addr, Handler: handler}, httpx.DefaultShutdownTimeout)
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
