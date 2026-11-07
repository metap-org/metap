//go:build integration

package control

// Ported from the Rust metap-control crate's tests/provisioning_postgres.rs
// (provision_schema_tenant_writes_registry_row_and_admin_user — the regression test for audit
// 06 finding #2, the highest-value case in that file: a freshly provisioned tenant's admin must
// remain resolvable by email alone through the shared pool, which is how tenant-less login
// works). The dedicated-db provisioning test needs a genuinely separate throwaway database (the
// Rust original creates one via the postgres maintenance database) — not ported here given this
// crate's overall size for this pass; the schema-tenant path above already exercises
// ProvisionSchemaTenant/CreateTenantSchema end to end.

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/metap-org/metap/pkg/testkit"
	"github.com/stretchr/testify/require"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestProvisionSchemaTenantWritesRegistryRowAndAdminUser(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	registry := NewPostgresTenantRegistry(pool)
	tenantID := uuid.New()
	email := "admin-" + strings.ReplaceAll(tenantID.String(), "-", "") + "@test.local"

	provisioned, err := ProvisionSchemaTenant(ctx, pool, registry, tenantID, email, "pass123")
	require.NoError(t, err)
	require.Equal(t, tenantID, provisioned.TenantID, "got %v", provisioned.TenantID)
	t.Cleanup(func() {
		ctx := context.Background()
		schema := "t_" + tenantSimpleHex(tenantID)
		pool.Exec(ctx, `DROP SCHEMA IF EXISTS "`+schema+`" CASCADE`)
		pool.Exec(ctx, "DELETE FROM user_roles WHERE tenant_id = $1", tenantID)
		pool.Exec(ctx, "DELETE FROM users WHERE tenant_id = $1", tenantID)
		pool.Exec(ctx, "DELETE FROM control.tenants WHERE id = $1", tenantID)
	})

	routing, err := registry.Get(ctx, tenantID)
	require.NoError(t, err, "expected a tenant row, err=")
	require.NotNil(t, routing, "expected a tenant row, err=%v", err)
	expectedSchema := "t_" + tenantSimpleHex(tenantID)
	require.Equal(t, StrategySchema, routing.Strategy.Kind, "real per-tenant schema isolation: schema_name must be %q, not \"public\" — got %+v", expectedSchema, routing.Strategy)
	require.Equal(t, expectedSchema, routing.Strategy.SchemaName, "real per-tenant schema isolation: schema_name must be %q, not \"public\" — got %+v", expectedSchema, routing.Strategy)

	// Identity stays in the shared metadata tables, deliberately — users/user_roles are
	// excluded from CreateTenantSchema's clone list (audit 06 finding #2).
	var role string
	require.NoError(t, pool.QueryRow(ctx, "SELECT role FROM user_roles WHERE tenant_id = $1 AND user_id = $2",
		tenantID, provisioned.AdminUserID).Scan(&role))
	require.Equal(t, "admin", role, "got role %q", role)

	// Regression test for audit 06 finding #2: POST /auth/login with no tenantId in the body
	// resolves the user on the *shared* pool with the database's default search_path — it
	// cannot know a tenant's schema before it has found the user, because finding the user is
	// how it learns the tenant. Querying unqualified through the bare pool here reproduces that
	// exact lookup.
	var foundTenant uuid.UUID
	require.NoError(t, pool.QueryRow(ctx, "SELECT tenant_id FROM users WHERE email = $1", email).Scan(&foundTenant))
	require.Equal(t, tenantID, foundTenant, "a freshly provisioned tenant's admin must be resolvable by email alone — got %v, want %v", foundTenant, tenantID)
}

// Regression test: ProvisionSchemaTenant used to hold the dedicated connection it acquires for
// seeding (SET search_path/CreateUser/AssignRole) open via defer conn.Release() until the whole
// function returned — but registry.Provision, called before that return, acquires a *second*
// connection from the very same pool. Against a pool with only 1 connection (a real, supported
// case — e.g. dev-tools' CLI subcommands, found live reproducing exactly this), that second
// acquire could never succeed while the first was still held: a permanent deadlock, not a slow
// path. Fixed by releasing the dedicated connection before calling registry.Provision. This test
// pins that fix down with a context deadline short enough that the old behavior would hang the
// whole test run rather than just fail an assertion.
func TestProvisionSchemaTenantDoesNotDeadlockASingleConnectionPool(t *testing.T) {
	pool := testkit.NewPostgres(t, testkit.WithSearchPath("metadata", "public"), func(cfg *pgxpool.Config) { cfg.MaxConns = 1 })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	registry := NewPostgresTenantRegistry(pool)
	tenantID := uuid.New()
	email := "admin-" + strings.ReplaceAll(tenantID.String(), "-", "") + "@test.local"

	provisioned, err := ProvisionSchemaTenant(ctx, pool, registry, tenantID, email, "pass123")
	if err != nil {
		require.NoError(t, ctx.Err(), "ProvisionSchemaTenant deadlocked against a 1-connection pool")
		require.FailNow(t, fmt.Sprint(err))
	}
	t.Cleanup(func() {
		ctx := context.Background()
		schema := "t_" + tenantSimpleHex(tenantID)
		pool.Exec(ctx, `DROP SCHEMA IF EXISTS "`+schema+`" CASCADE`)
		pool.Exec(ctx, "DELETE FROM user_roles WHERE tenant_id = $1", tenantID)
		pool.Exec(ctx, "DELETE FROM users WHERE tenant_id = $1", tenantID)
		pool.Exec(ctx, "DELETE FROM control.tenants WHERE id = $1", tenantID)
	})

	require.Equal(t, tenantID, provisioned.TenantID, "got %v", provisioned.TenantID)
}
