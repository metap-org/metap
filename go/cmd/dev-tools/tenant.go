package main

import (
	"context"
	"fmt"
	"os"

	"github.com/metap-org/metap-go/control"
	"github.com/metap-org/metap-go/peripherals"
)

// seedAdmin mirrors seed-admin.mjs, via peripherals.AssignRole (the same function the
// peripherals package's own e2e tests already verify against the real dev Postgres) instead of
// a hand-rolled query.
func seedAdmin(args []string) error {
	if len(args) < 4 {
		fmt.Fprintln(os.Stderr, "Usage: dev-tools seed-admin <tenantId> <userId>")
		os.Exit(1)
	}
	ctx := context.Background()
	pool, err := connectShared(ctx, 1)
	if err != nil {
		return err
	}
	defer pool.Close()

	tenantID, err := parseUUID(args[2])
	if err != nil {
		return err
	}
	userID, err := parseUUID(args[3])
	if err != nil {
		return err
	}

	router := routerFor(pool)
	tx, err := router.Begin(ctx, tenantID)
	if err != nil {
		return fmt.Errorf("failed to resolve tenant %s's database: %w", tenantID, err)
	}
	defer tx.Rollback(ctx)
	if err := peripherals.AssignRole(ctx, tx, tenantID, userID, "admin", nil); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}

	fmt.Printf("Granted 'admin' role to user %s in tenant %s.\n", userID, tenantID)
	return nil
}

// createUser is the dev-seeding counterpart to POST /admin/users — both call
// peripherals.CreateUser, so a seeded dev user and an admin-provisioned one get their password
// hashed identically.
func createUser(args []string) error {
	if len(args) < 5 {
		fmt.Fprintln(os.Stderr, "Usage: dev-tools create-user <tenantId> <email> <password>")
		os.Exit(1)
	}
	ctx := context.Background()
	pool, err := connectShared(ctx, 1)
	if err != nil {
		return err
	}
	defer pool.Close()

	tenantID, err := parseUUID(args[2])
	if err != nil {
		return err
	}
	email, password := args[3], args[4]

	router := routerFor(pool)
	tx, err := router.Begin(ctx, tenantID)
	if err != nil {
		return fmt.Errorf("failed to resolve tenant %s's database: %w", tenantID, err)
	}
	defer tx.Rollback(ctx)
	user, err := peripherals.CreateUser(ctx, tx, tenantID, email, password)
	if err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}

	fmt.Printf("Created user %s (%s) in tenant %s.\n", user.ID, email, tenantID)
	return nil
}

// provisionTenant registers a control.tenants row and creates the tenant's first admin user —
// the bootstrap path for a tenant, mirroring how seedAdmin/createUser above already bootstrap a
// user/role without going through an admin-authorized HTTP call (there's no admin yet to
// authorize one).
func provisionTenant(args []string) error {
	ctx := context.Background()
	pool, err := connectShared(ctx, 1)
	if err != nil {
		return err
	}
	defer pool.Close()
	registry := control.NewPostgresTenantRegistry(pool)

	var strategy string
	if len(args) > 3 {
		strategy = args[3]
	}

	switch strategy {
	case "schema":
		if len(args) < 6 {
			fmt.Fprintln(os.Stderr, "Usage: dev-tools provision-tenant <tenantId> schema <adminEmail> <adminPassword>")
			os.Exit(1)
		}
		tenantID, err := parseUUID(args[2])
		if err != nil {
			return err
		}
		email, password := args[4], args[5]

		provisioned, err := control.ProvisionSchemaTenant(ctx, pool, registry, tenantID, email, password)
		if err != nil {
			return err
		}
		fmt.Printf("Provisioned tenant %s (trial, schema=t_<tenantId>), admin user %s (%s).\n",
			tenantID, provisioned.AdminUserID, email)
		printDenyByDefaultNote()

	case "dedicated_db":
		if len(args) < 8 {
			fmt.Fprintln(os.Stderr, "Usage: dev-tools provision-tenant <tenantId> dedicated_db <dsnSecretRefName> <dedicatedDatabaseUrl> <adminEmail> <adminPassword>")
			os.Exit(1)
		}
		tenantID, err := parseUUID(args[2])
		if err != nil {
			return err
		}
		dsnSecretRef, dedicatedURL, email, password := args[4], args[5], args[6], args[7]

		provisioned, err := control.ProvisionDedicatedDbTenant(ctx, registry, tenantID, dsnSecretRef, dedicatedURL, migrationsDir(), email, password)
		if err != nil {
			return err
		}
		fmt.Printf("Provisioned tenant %s (paid, dedicated_db), admin user %s (%s).\n",
			tenantID, provisioned.AdminUserID, email)
		fmt.Printf("Before this tenant can be routed to, set env var %s=%s for the owning app's process "+
			"(Router's EnvStore looks it up by this exact name).\n", dsnSecretRef, dedicatedURL)
		printDenyByDefaultNote()

	default:
		fmt.Fprintln(os.Stderr, "Usage: dev-tools provision-tenant <tenantId> <schema|dedicated_db> ...")
		os.Exit(1)
	}
	return nil
}

// bootstrapPlatformAdmin bootstraps the very first platform-superadmin — same con-gà-quả-trứng
// bootstrap as seedAdmin/createUser above, CLI-only because no platform-admin exists yet to
// authorize an HTTP call. Creates a user in control.PlatformTenantID (a sentinel tenant, never a
// real control.tenants row) and grants it the "platform_admin" role.
func bootstrapPlatformAdmin(args []string) error {
	if len(args) < 4 {
		fmt.Fprintln(os.Stderr, "Usage: dev-tools bootstrap-platform-admin <email> <password>")
		os.Exit(1)
	}
	ctx := context.Background()
	pool, err := connectShared(ctx, 1)
	if err != nil {
		return err
	}
	defer pool.Close()

	email, password := args[2], args[3]
	tenantID := control.PlatformTenantID

	user, err := peripherals.CreateUser(ctx, pool, tenantID, email, password)
	if err != nil {
		return err
	}
	if err := peripherals.AssignRole(ctx, pool, tenantID, user.ID, "platform_admin", nil); err != nil {
		return err
	}

	fmt.Printf("Created platform-admin user %s (%s).\n", user.ID, email)
	fmt.Printf("Mint a token for it with: dev-tools mint-token %s %s\n", tenantID, user.ID)
	return nil
}

// setTenantHostname maps a hostname onto a tenant, so GET /public/config can serve that
// tenant's branding on its login screen before anyone has logged in.
//
// Operator-only, and there is deliberately no HTTP equivalent — a tenant able to claim an
// arbitrary hostname could claim another tenant's and serve its own branding there. Writes to
// the shared DATABASE_URL pool, not a tenant-routed one: control.tenant_hostnames has to be
// readable *before* a tenant is known, which is the whole point of the lookup.
func setTenantHostname(args []string) error {
	if len(args) < 4 {
		fmt.Fprintln(os.Stderr, "Usage: dev-tools set-tenant-hostname <tenantId> <hostname>")
		os.Exit(1)
	}
	ctx := context.Background()
	pool, err := connectShared(ctx, 1)
	if err != nil {
		return err
	}
	defer pool.Close()

	tenantID, err := parseUUID(args[2])
	if err != nil {
		return err
	}
	hostname := args[3]

	stored, err := control.SetTenantHostname(ctx, pool, tenantID, hostname)
	if err != nil {
		return err
	}
	fmt.Printf("Hostname %q now resolves to tenant %s for GET /public/config.\n", stored, tenantID)
	if stored != hostname {
		fmt.Printf("(normalized from %q — lowercased, port and trailing dot removed)\n", hostname)
	}
	return nil
}
