// Tenant provisioning — the two flows a CLI (not yet ported) has always run inline, pulled out
// here so a future HTTP handler can call the exact same functions instead of a second,
// hand-copied implementation.
package control

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/metap-org/metap-go/dbmigrate"
	"github.com/metap-org/metap-go/infra"
	"github.com/metap-org/metap-go/peripherals"
)

type ProvisionedTenant struct {
	TenantID    uuid.UUID
	AdminUserID uuid.UUID
}

// seedLocalAuthConfig: every newly provisioned tenant gets local (password) auth enabled by
// default — the only provider that has ever existed. A tenant can have more than one provider
// enabled; this is just what every tenant starts with.
func seedLocalAuthConfig(ctx context.Context, db DBTX, tenantID uuid.UUID) error {
	_, err := db.Exec(ctx,
		"INSERT INTO tenant_auth_configs (tenant_id, provider_kind, enabled, config) VALUES ($1, 'local', true, '{}'::jsonb) ON CONFLICT (tenant_id, provider_kind) DO NOTHING",
		tenantID)
	return err
}

// ProvisionSchemaTenant is the trial tier — real per-tenant schema isolation: schemaName is
// "t_" + tenantID (hex, no dashes) — satisfies ValidateSchemaName's ^t_[a-z0-9]+$ whitelist by
// construction, deterministic, collision-free 1:1 with the tenant's own id. CreateTenantSchema
// does the DDL (creates the schema, clones every tenant-scoped table into it) before the
// control.tenants row is written — finish all setup, then make the tenant visible to Router in
// one atomic insert, never a row that exists before its schema does.
func ProvisionSchemaTenant(ctx context.Context, sharedPool *pgxpool.Pool, registry *PostgresTenantRegistry, tenantID uuid.UUID, adminEmail, adminPassword string) (*ProvisionedTenant, error) {
	schemaName := "t_" + tenantSimpleHex(tenantID)
	if err := CreateTenantSchema(ctx, sharedPool, schemaName); err != nil {
		return nil, err
	}

	// A dedicated connection, not the pool directly — a plain (session-level, not transaction-
	// scoped) SET is fine only because this connection is acquired, used for this provisioning
	// call alone, and released before registry.Provision runs (never held across a second call
	// into the same pool, and never returned to serve an unrelated request afterward with a
	// stale search_path). Releasing explicitly here, not via defer, matters concretely: registry
	// wraps this same sharedPool, and a caller that opened sharedPool with a small MaxConns (a
	// single-connection pool is a real, supported case — e.g. dev-tools' CLI subcommands) would
	// deadlock waiting for a second connection that only this held-open one could ever free,
	// found live by reproducing exactly that call shape against a 1-connection pool.
	conn, err := sharedPool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := conn.Exec(ctx, fmt.Sprintf(`SET search_path TO %q, metadata, control`, schemaName)); err != nil {
		conn.Release()
		return nil, err
	}
	if err := seedLocalAuthConfig(ctx, conn, tenantID); err != nil {
		conn.Release()
		return nil, err
	}
	user, err := peripherals.CreateUser(ctx, conn, tenantID, adminEmail, adminPassword)
	if err != nil {
		conn.Release()
		return nil, err
	}
	if err := peripherals.AssignRole(ctx, conn, tenantID, user.ID, "admin", nil); err != nil {
		conn.Release()
		return nil, err
	}
	conn.Release()

	if err := registry.Provision(ctx, tenantID, "trial", "schema", &schemaName, nil, "active"); err != nil {
		return nil, err
	}

	return &ProvisionedTenant{TenantID: tenantID, AdminUserID: user.ID}, nil
}

// ProvisionDedicatedDbTenant is the paid tier — migrates dedicatedDatabaseURL (a fresh Postgres
// database) with the same migrations/*.sql the main database runs, so
// records/users/... exist there too, then writes the control.tenants row and creates the admin
// user on the dedicated database. The caller is responsible for making dsnSecretRef resolvable
// (EnvStore reads it as an env var name) before this tenant is actually routed to — this
// function only writes the registry row, it doesn't touch the running process's environment.
func ProvisionDedicatedDbTenant(ctx context.Context, registry *PostgresTenantRegistry, tenantID uuid.UUID, dsnSecretRef, dedicatedDatabaseURL, migrationsDir, adminEmail, adminPassword string) (*ProvisionedTenant, error) {
	dedicatedPool, err := infra.ConnectForMigrate(ctx, dedicatedDatabaseURL)
	if err != nil {
		return nil, err
	}
	defer dedicatedPool.Close()

	if _, err := dbmigrate.Run(ctx, dedicatedPool, migrationsDir); err != nil {
		return nil, err
	}
	// control.tenants is genuinely global platform data — the one registry every tenant is
	// looked up through — never tenant-scoped data itself. There's no way to skip a migration
	// file, so drop the control schema right back out post-migrate — the dedicated DB's own
	// migration-tracking table still records it as applied, which is correct (it did run);
	// nothing here ever migrates that table again.
	if _, err := dedicatedPool.Exec(ctx, "DROP SCHEMA IF EXISTS control CASCADE"); err != nil {
		return nil, err
	}

	if err := registry.Provision(ctx, tenantID, "paid", "dedicated_db", nil, &dsnSecretRef, "active"); err != nil {
		return nil, err
	}
	if err := seedLocalAuthConfig(ctx, dedicatedPool, tenantID); err != nil {
		return nil, err
	}
	user, err := peripherals.CreateUser(ctx, dedicatedPool, tenantID, adminEmail, adminPassword)
	if err != nil {
		return nil, err
	}
	if err := peripherals.AssignRole(ctx, dedicatedPool, tenantID, user.ID, "admin", nil); err != nil {
		return nil, err
	}

	return &ProvisionedTenant{TenantID: tenantID, AdminUserID: user.ID}, nil
}

// tenantSimpleHex is the 32-lowercase-hex-char form of a UUID (no dashes) — the Go equivalent of
// Rust's Uuid::simple().
func tenantSimpleHex(id uuid.UUID) string {
	b := id[:]
	const hex = "0123456789abcdef"
	out := make([]byte, 32)
	for i, c := range b {
		out[i*2] = hex[c>>4]
		out[i*2+1] = hex[c&0xf]
	}
	return string(out)
}
