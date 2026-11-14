package app

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/metap-org/metap/pkg/bootstrap"
	"github.com/metap-org/metap/pkg/control"
	"github.com/metap-org/metap/pkg/infra"
	"github.com/metap-org/metap/pkg/metadata"
	"github.com/metap-org/metap/pkg/peripherals"
	"github.com/metap-org/metap/pkg/permission"
	"github.com/metap-org/metap/pkg/secrets"
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
		slog.Error(opts.Name+" failed", "error", err)
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
func RunBootstrap(ctx context.Context, opts BootstrapOptions) error {
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

	registry := metadata.NewMetadataRegistry()
	if err := registry.RegisterAllSubmitted(); err != nil {
		return fmt.Errorf("registering entities: %w", err)
	}

	decl, err := declaration(opts.Extra)
	if err != nil {
		return err
	}

	tenants, err := bootstrapTargets(ctx, pool, os.Getenv("BOOTSTRAP_TENANT_IDS"))
	if err != nil {
		return err
	}
	return ApplyBootstrap(ctx, registry, permissions, router, tenants, decl, config.NodeEnv == infra.NodeEnvProduction, opts.Name)
}

// declaration is everything the binary submitted, plus extra, plus the BOOTSTRAP_FILE overlay.
func declaration(extra *bootstrap.Bootstrap) (bootstrap.Bootstrap, error) {
	decl := bootstrap.Collect()
	if extra != nil {
		decl = bootstrap.Merge(decl, *extra)
	}
	if path := os.Getenv("BOOTSTRAP_FILE"); path != "" {
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
	router *control.Router, tenants []uuid.UUID, decl bootstrap.Bootstrap, production bool, name string) error {
	deps := bootstrap.Deps{
		Registry: registry,
		Policies: permissions,
		Identity: bootstrap.ScopedIdentity{Pool: func(ctx context.Context, tenantID uuid.UUID) (peripherals.DBTX, error) {
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
	summaries, err := control.NewPostgresTenantRegistry(pool).List(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing tenants: %w", err)
	}
	var out []uuid.UUID
	for _, s := range summaries {
		if s.Status == control.StatusActive {
			out = append(out, s.ID)
		}
	}
	return out, nil
}
