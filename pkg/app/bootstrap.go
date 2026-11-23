package app

import (
	"context"
	"fmt"
	"github.com/metap-org/metap/pkg/permission/policypg"
	"github.com/metap-org/metap/pkg/permission/rolestore"
	"log/slog"
	"os"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"

	"github.com/metap-org/metap/pkg/bootstrap"
	"github.com/metap-org/metap/pkg/config"
	"github.com/metap-org/metap/pkg/infra"
	"github.com/metap-org/metap/pkg/kit/env"
	"github.com/metap-org/metap/pkg/kit/standard"
	"github.com/metap-org/metap/pkg/metadata"
	"github.com/metap-org/metap/pkg/permission"
	"github.com/metap-org/metap/pkg/secrets/backend"
	"github.com/metap-org/metap/pkg/tenancy"
)

// BootstrapOptions configure RunBootstrap.
type BootstrapOptions struct {
	// Name labels log lines, e.g. "waf-bootstrap".
	Name string
	// Extra is merged after everything the binary's packages submitted with bootstrap.Submit.
	Extra *bootstrap.Bootstrap
}

// BootstrapMain runs RunBootstrap and exits the process with status 1 on any error. A product
// ships one tiny main that blank-imports every service's entities/bootstrap packages and calls
// this — the one-shot counterpart to db-migrate.
func BootstrapMain(opts BootstrapOptions) {
	if err := RunBootstrap(context.Background(), opts); err != nil {
		slog.Error(opts.Name+" failed", standard.LogError, err)
		os.Exit(1)
	}
}

// RunBootstrap applies the collected declaration to every target tenant and returns. Environment:
//
//	BOOTSTRAP_TENANT_IDS  comma-separated tenant ids; unset or "all" means every active tenant
//	BOOTSTRAP_FILE        optional JSON overlay merged after the code-declared parts
//	<SecretRef>         one environment variable per declared user (see bootstrap.EnvPasswords)
//
// NODE_ENV=production makes a missing password for a new user fatal.
//
// It reads only what it uses (DATABASE_URL, NODE_ENV and the secret-store variables), not
// infra.LoadConfig: a one-shot that only writes grants and users needs no RabbitMQ or token keys.
func RunBootstrap(ctx context.Context, opts BootstrapOptions) error {
	_ = godotenv.Load()
	if err := config.LoadFileFromEnv(); err != nil {
		return err
	}
	databaseURL, err := env.Require("DATABASE_URL")
	if err != nil {
		return err
	}
	pool, err := infra.Connect(ctx, databaseURL)
	if err != nil {
		return fmt.Errorf("connecting to postgres: %w", err)
	}
	defer pool.Close()
	secretManager, err := backend.NewManager(ctx, backend.ConfigFromEnv(), pool)
	if err != nil {
		return fmt.Errorf("building secret store: %w", err)
	}
	router := tenancy.NewRouter(pool, tenancy.NewRegistryCache(tenancy.NewPostgresTenantRegistry(pool)), secretManager)
	permissions := permission.NewPermissionService(policypg.NewPostgresPolicyStore(router))

	registry := metadata.NewMetadataRegistry()
	if err := registry.RegisterAllSubmitted(); err != nil {
		return fmt.Errorf("registering entities: %w", err)
	}

	decl, err := declaration(opts.Extra)
	if err != nil {
		return err
	}

	tenants, err := bootstrapTargets(ctx, pool, env.String("BOOTSTRAP_TENANT_IDS", ""))
	if err != nil {
		return err
	}
	return ApplyBootstrap(ctx, registry, permissions, router, tenants, decl, env.String("NODE_ENV", "") == "production", opts.Name)
}

// declaration is everything the binary submitted, plus extra, plus the BOOTSTRAP_FILE overlay.
func declaration(extra *bootstrap.Bootstrap) (bootstrap.Bootstrap, error) {
	decl := bootstrap.Collect()
	if extra != nil {
		decl = bootstrap.Merge(decl, *extra)
	}
	if path := env.String("BOOTSTRAP_FILE", ""); path != "" {
		overlay, err := bootstrap.LoadFile(path)
		if err != nil {
			return decl, err
		}
		decl = bootstrap.Merge(decl, overlay)
	}
	return decl, nil
}

// ApplyBootstrap applies decl to each tenant, stopping at the first error.
func ApplyBootstrap(ctx context.Context, registry *metadata.MetadataRegistry, permissions *permission.PermissionService,
	router *tenancy.Router, tenants []uuid.UUID, decl bootstrap.Bootstrap, production bool, name string) error {
	deps := bootstrap.Deps{
		Registry: registry,
		Policies: permissions,
		Identity: bootstrap.ScopedIdentity{Shared: router.SharedPool(), Pool: func(ctx context.Context, tenantID uuid.UUID) (rolestore.DBTX, error) {
			return router.PoolFor(ctx, tenantID)
		}},
	}
	for _, tenantID := range tenants {
		res, err := bootstrap.Apply(ctx, deps, tenantID, decl, bootstrap.Options{Production: production})
		if err != nil {
			return fmt.Errorf("tenant %s: %w", tenantID, err)
		}
		slog.Info(name+" applied", "tenant", tenantID,
			"policies_created", res.PoliciesCreated, "policies_existing", res.PoliciesExisting,
			"users_created", len(res.UsersCreated), "users_existing", len(res.UsersExisting), "users_skipped", len(res.UsersSkipped))
	}
	return nil
}

func bootstrapTargets(ctx context.Context, pool *pgxpool.Pool, raw string) ([]uuid.UUID, error) {
	raw = strings.TrimSpace(raw)
	if raw != "" && raw != "all" {
		var out []uuid.UUID
		for _, part := range strings.Split(raw, ",") {
			id, err := uuid.Parse(strings.TrimSpace(part))
			if err != nil {
				return nil, fmt.Errorf("BOOTSTRAP_TENANT_IDS: %w", err)
			}
			out = append(out, id)
		}
		return out, nil
	}
	summaries, err := tenancy.NewPostgresTenantRegistry(pool).List(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing tenants: %w", err)
	}
	var out []uuid.UUID
	for _, s := range summaries {
		if s.Status == tenancy.StatusActive {
			out = append(out, s.ID)
		}
	}
	return out, nil
}
