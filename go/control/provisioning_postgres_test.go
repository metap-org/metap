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
	"strings"
	"testing"

	"github.com/google/uuid"
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
