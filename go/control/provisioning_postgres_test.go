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
	"os"
	"strings"
	"testing"
	"time"

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
	if err != nil {
		t.Fatal(err)
	}
	if provisioned.TenantID != tenantID {
		t.Fatalf("got %v", provisioned.TenantID)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		schema := "t_" + tenantSimpleHex(tenantID)
		pool.Exec(ctx, `DROP SCHEMA IF EXISTS "`+schema+`" CASCADE`)
		pool.Exec(ctx, "DELETE FROM user_roles WHERE tenant_id = $1", tenantID)
		pool.Exec(ctx, "DELETE FROM users WHERE tenant_id = $1", tenantID)
		pool.Exec(ctx, "DELETE FROM control.tenants WHERE id = $1", tenantID)
	})

	routing, err := registry.Get(ctx, tenantID)
	if err != nil || routing == nil {
		t.Fatalf("expected a tenant row, err=%v", err)
	}
	expectedSchema := "t_" + tenantSimpleHex(tenantID)
	if routing.Strategy.Kind != StrategySchema || routing.Strategy.SchemaName != expectedSchema {
		t.Fatalf("real per-tenant schema isolation: schema_name must be %q, not \"public\" — got %+v",
			expectedSchema, routing.Strategy)
	}

	// Identity stays in the shared metadata tables, deliberately — users/user_roles are
	// excluded from CreateTenantSchema's clone list (audit 06 finding #2).
	var role string
	if err := pool.QueryRow(ctx, "SELECT role FROM user_roles WHERE tenant_id = $1 AND user_id = $2",
		tenantID, provisioned.AdminUserID).Scan(&role); err != nil {
		t.Fatal(err)
	}
	if role != "admin" {
		t.Fatalf("got role %q", role)
	}

	// Regression test for audit 06 finding #2: POST /auth/login with no tenantId in the body
	// resolves the user on the *shared* pool with the database's default search_path — it
	// cannot know a tenant's schema before it has found the user, because finding the user is
	// how it learns the tenant. Querying unqualified through the bare pool here reproduces that
	// exact lookup.
	var foundTenant uuid.UUID
	if err := pool.QueryRow(ctx, "SELECT tenant_id FROM users WHERE email = $1", email).Scan(&foundTenant); err != nil {
		t.Fatal(err)
	}
	if foundTenant != tenantID {
		t.Fatalf("a freshly provisioned tenant's admin must be resolvable by email alone — got %v, want %v",
			foundTenant, tenantID)
	}
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
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		url = "postgres://metap:metap@localhost:5432/metap"
	}
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Skipf("no Postgres available (%v) — skipping e2e test", err)
	}
	defer pool.Close()
	if err := pool.Ping(context.Background()); err != nil {
		t.Skipf("no Postgres available (%v) — skipping e2e test", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	registry := NewPostgresTenantRegistry(pool)
	tenantID := uuid.New()
	email := "admin-" + strings.ReplaceAll(tenantID.String(), "-", "") + "@test.local"

	provisioned, err := ProvisionSchemaTenant(ctx, pool, registry, tenantID, email, "pass123")
	if err != nil {
		if ctx.Err() != nil {
			t.Fatal("ProvisionSchemaTenant deadlocked against a 1-connection pool")
		}
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		schema := "t_" + tenantSimpleHex(tenantID)
		pool.Exec(ctx, `DROP SCHEMA IF EXISTS "`+schema+`" CASCADE`)
		pool.Exec(ctx, "DELETE FROM user_roles WHERE tenant_id = $1", tenantID)
		pool.Exec(ctx, "DELETE FROM users WHERE tenant_id = $1", tenantID)
		pool.Exec(ctx, "DELETE FROM control.tenants WHERE id = $1", tenantID)
	})

	if provisioned.TenantID != tenantID {
		t.Fatalf("got %v", provisioned.TenantID)
	}
}
