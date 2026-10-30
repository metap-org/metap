// dev-tools is a small CLI of dev/ops subcommands — ported from metap's Rust metap-dev-tools
// binary. Every subcommand delegates to the exact same library functions a real HTTP path uses
// (peripherals.CreateUser/AssignRole, control.ProvisionSchemaTenant/ProvisionDedicatedDbTenant,
// peripherals.MintJWT, ...) so a CLI-seeded user/tenant/token can never diverge from what a real
// login/admin-provisioned one looks like.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"

	"github.com/metap-org/metap-go/control"
	"github.com/metap-org/metap-go/runtime"
)

func usage() {
	fmt.Fprintln(os.Stderr, "Usage:")
	fmt.Fprintln(os.Stderr, "  dev-tools gen-keys [dir]                         (default dir: ./keys)")
	fmt.Fprintln(os.Stderr, "  dev-tools gen-jwks-key [dir] [kid]                (EdDSA via jwks, default dir: ./keys, default kid: a fresh UUID)")
	fmt.Fprintln(os.Stderr, "  dev-tools mint-token [tenantId] [userId]         (RS256, reads ./keys/dev-jwt-private.pem)")
	fmt.Fprintln(os.Stderr, "  dev-tools seed-admin <tenantId> <userId>")
	fmt.Fprintln(os.Stderr, "  dev-tools create-user <tenantId> <email> <password>  (local login, argon2id)")
	fmt.Fprintln(os.Stderr, "  dev-tools provision-tenant <tenantId> schema <adminEmail> <adminPassword>")
	fmt.Fprintln(os.Stderr, "  dev-tools provision-tenant <tenantId> dedicated_db <dsnSecretRefName> <dedicatedDatabaseUrl> <adminEmail> <adminPassword>")
	fmt.Fprintln(os.Stderr, "  dev-tools bootstrap-platform-admin <email> <password>")
	fmt.Fprintln(os.Stderr, "  dev-tools vault-put-dsn <dsnSecretRef> <dsn>                     (reads VAULT_ADDR/VAULT_TOKEN)")
	fmt.Fprintln(os.Stderr, "  dev-tools aws-secrets-put-dsn <dsnSecretRef> <dsn>               (reads AWS_SECRETS_REGION/_ACCESS_KEY/_SECRET_KEY)")
	fmt.Fprintln(os.Stderr, "  dev-tools gcp-secrets-put-dsn <dsnSecretRef> <dsn>               (reads GCP_SECRETS_PROJECT_ID)")
	fmt.Fprintln(os.Stderr, "  dev-tools enqueue-reconcile <tenantId> <entityName> <desiredVersion>")
	fmt.Fprintln(os.Stderr, "  dev-tools set-tenant-hostname <tenantId> <hostname>")
	fmt.Fprintln(os.Stderr, "  dev-tools put-tenant-secret <tenantId> <configKey> <value>")
	fmt.Fprintln(os.Stderr, "  dev-tools migrate-to-dedicated-table <tenantId> <entityJsonPath> [sourceTable]")
	fmt.Fprintln(os.Stderr, "  dev-tools reconcile-plan <tenantId> <entityJsonPath> <tableName>")
	os.Exit(1)
}

func main() {
	args := os.Args
	var sub string
	if len(args) > 1 {
		sub = args[1]
	}

	var err error
	switch sub {
	case "gen-keys":
		err = genKeys(argOr(args, 2, "keys"))
	case "gen-jwks-key":
		err = genJwksKey(argOr(args, 2, "keys"), argOr(args, 3, uuid.New().String()))
	case "mint-token":
		err = mintToken(args)
	case "seed-admin":
		err = seedAdmin(args)
	case "create-user":
		err = createUser(args)
	case "provision-tenant":
		err = provisionTenant(args)
	case "bootstrap-platform-admin":
		err = bootstrapPlatformAdmin(args)
	case "vault-put-dsn":
		err = vaultPutDSN(args)
	case "aws-secrets-put-dsn":
		err = awsSecretsPutDSN(args)
	case "gcp-secrets-put-dsn":
		err = gcpSecretsPutDSN(args)
	case "enqueue-reconcile":
		err = enqueueReconcile(args)
	case "set-tenant-hostname":
		err = setTenantHostname(args)
	case "put-tenant-secret":
		err = putTenantSecret(args)
	case "migrate-to-dedicated-table":
		err = migrateToDedicatedTable(args)
	case "reconcile-plan":
		err = reconcilePlan(args)
	default:
		usage()
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}

func argOr(args []string, i int, def string) string {
	if i < len(args) {
		return args[i]
	}
	return def
}

// migrationsDir mirrors cmd/db-migrate's own fallback (MIGRATIONS_DIR env override, else
// "../crates/migrations" relative to this binary's conventional go/ invocation directory).
func migrationsDir() string {
	if dir, ok := runtime.Optional("MIGRATIONS_DIR"); ok {
		return dir
	}
	return "../crates/migrations"
}

// connectShared loads .env (if present) and opens a pool against DATABASE_URL with maxConns
// connections — most subcommands only ever need 1 connection; a couple (migrate-to-dedicated-
// table, reconcile-plan) hold one connection for an advisory lock for the whole duration of a
// reconcile while issuing further queries against the same pool, which deadlocks a 1-connection
// pool the moment the resolved tenant's strategy is Schema (PoolFor returns this exact pool, not
// a fresh one).
func connectShared(ctx context.Context, maxConns int32) (*pgxpool.Pool, error) {
	_ = godotenv.Load()
	databaseURL, err := runtime.RequireEnv("DATABASE_URL")
	if err != nil {
		return nil, err
	}
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, err
	}
	cfg.MaxConns = maxConns
	return pgxpool.NewWithConfig(ctx, cfg)
}

// routerFor builds the same kind of Router a real app's own main.go does, so seed-admin/
// create-user/enqueue-reconcile/migrate-to-dedicated-table/reconcile-plan can't resolve a
// tenant's database any differently than a real request would. EnvStore only (not the AppRole/
// Vault branching a real app's main.go has) — a Vault-backed dedicated tenant's DSN can still be
// seeded by hand via vault-put-dsn first, same as it always could.
func routerFor(sharedPool *pgxpool.Pool) *control.Router {
	tenantRegistry := control.NewPostgresTenantRegistry(sharedPool)
	return control.NewRouter(sharedPool, control.NewRegistryCache(tenantRegistry), control.EnvStore{})
}

func parseUUID(s string) (uuid.UUID, error) {
	id, err := uuid.Parse(s)
	if err != nil {
		return uuid.UUID{}, fmt.Errorf("invalid UUID %q: %w", s, err)
	}
	return id, nil
}

func printDenyByDefaultNote() {
	fmt.Println("Note: no policies exist yet for this tenant — PermissionService denies any action on " +
		"an entity/action pair with zero policies for non-admin callers (deny-by-default, " +
		"admin always bypasses). The admin user just created can already do everything; any " +
		"other user in this tenant is denied everything until you grant policies, e.g. " +
		"createPolicy / a seed-defaults mutation.")
}
