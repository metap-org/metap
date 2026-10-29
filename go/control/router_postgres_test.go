package control

// Ported from the Rust metap-control crate's tests/router_postgres.rs.

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func testPool(t *testing.T) *pgxpool.Pool {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		url = "postgres://metap:metap@localhost:5432/metap"
	}
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, "SET search_path TO metadata, public")
		return err
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Skipf("no Postgres available (%v) — skipping e2e test", err)
	}
	if err := pool.Ping(context.Background()); err != nil {
		t.Skipf("no Postgres available (%v) — skipping e2e test", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func testRouter(pool *pgxpool.Pool) *Router {
	registry := NewPostgresTenantRegistry(pool)
	return NewRouter(pool, NewRegistryCache(registry), EnvStore{})
}

func searchPath(ctx context.Context, tx pgx.Tx) string {
	var sp string
	tx.QueryRow(ctx, "SHOW search_path").Scan(&sp)
	return sp
}

func insertTenant(t *testing.T, pool *pgxpool.Pool, id uuid.UUID, strategy string, schemaName *string, status string) {
	_, err := pool.Exec(context.Background(),
		"INSERT INTO control.tenants (id, tier, strategy, schema_name, status) VALUES ($1, 'trial', $2, $3, $4)",
		id, strategy, schemaName, status)
	if err != nil {
		t.Fatal(err)
	}
}

func insertDedicatedTenant(t *testing.T, pool *pgxpool.Pool, id uuid.UUID, dsnSecretRef string) {
	_, err := pool.Exec(context.Background(),
		"INSERT INTO control.tenants (id, tier, strategy, dsn_secret_ref, status) VALUES ($1, 'paid', 'dedicated_db', $2, 'active')",
		id, dsnSecretRef)
	if err != nil {
		t.Fatal(err)
	}
}

func cleanupTenant(pool *pgxpool.Pool, id uuid.UUID) {
	pool.Exec(context.Background(), "DELETE FROM control.tenants WHERE id = $1", id)
}

func TestUnregisteredTenantFallsBackToPublicSchema(t *testing.T) {
	pool := testPool(t)
	router := testRouter(pool)
	tenantID := uuid.New() // never inserted into control.tenants

	tx, err := router.Begin(context.Background(), tenantID)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if !strings.Contains(searchPath(context.Background(), tx), "public") {
		t.Fatal("expected the public schema in search_path")
	}
}

func TestRegisteredSchemaTenantRoutesToItsSchemaAndDoesNotLeak(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tenantID := uuid.New()
	schemaName := "t_test1"
	insertTenant(t, pool, tenantID, "schema", &schemaName, "active")
	defer cleanupTenant(pool, tenantID)
	if _, err := pool.Exec(ctx, "CREATE SCHEMA IF NOT EXISTS t_test1"); err != nil {
		t.Fatal(err)
	}
	defer pool.Exec(context.Background(), "DROP SCHEMA t_test1")

	router := testRouter(pool)
	tx, err := router.Begin(ctx, tenantID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(searchPath(ctx, tx), "t_test1") {
		t.Fatal("expected t_test1 in search_path")
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	// A transaction opened directly on the same pool, bypassing Router, must NOT see the
	// previous transaction's SET LOCAL search_path — proves it was transaction-scoped, not
	// leaked onto the pooled physical connection.
	plainTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer plainTx.Rollback(ctx)
	if strings.Contains(searchPath(ctx, plainTx), "t_test1") {
		t.Fatal("search_path leaked across transactions on the same pooled connection")
	}
}

func TestSuspendedTenantIsRejected(t *testing.T) {
	pool := testPool(t)
	tenantID := uuid.New()
	public := "public"
	insertTenant(t, pool, tenantID, "schema", &public, "suspended")
	defer cleanupTenant(pool, tenantID)

	router := testRouter(pool)
	_, err := router.Begin(context.Background(), tenantID)
	re, ok := err.(*RouterError)
	if !ok || re.Kind != ErrTenantSuspended {
		t.Fatalf("expected ErrTenantSuspended, got %v", err)
	}
}

func TestMigratingTenantIsRejected(t *testing.T) {
	pool := testPool(t)
	tenantID := uuid.New()
	public := "public"
	insertTenant(t, pool, tenantID, "schema", &public, "migrating")
	defer cleanupTenant(pool, tenantID)

	router := testRouter(pool)
	_, err := router.Begin(context.Background(), tenantID)
	re, ok := err.(*RouterError)
	if !ok || re.Kind != ErrTenantMigrating {
		t.Fatalf("expected ErrTenantMigrating, got %v", err)
	}
}

func TestMaliciousSchemaNameIsRejectedBeforeUse(t *testing.T) {
	pool := testPool(t)
	tenantID := uuid.New()
	malicious := "public; DROP TABLE control.tenants;--"
	insertTenant(t, pool, tenantID, "schema", &malicious, "active")
	defer cleanupTenant(pool, tenantID)

	router := testRouter(pool)
	_, err := router.Begin(context.Background(), tenantID)
	re, ok := err.(*RouterError)
	if !ok || re.Kind != ErrInvalidSchemaName {
		t.Fatalf("expected ErrInvalidSchemaName, got %v", err)
	}
}

// There's no second real Postgres database available in this dev environment, so this points
// dsnSecretRef at an env var holding the *same* DATABASE_URL the rest of this file already
// connects to — enough to exercise the full path (EnvStore lookup -> new pool -> Begin), just
// not a genuinely separate database.
func TestDedicatedDbTenantRoutesThroughSecretStoreToItsOwnPool(t *testing.T) {
	pool := testPool(t)
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		databaseURL = "postgres://metap:metap@localhost:5432/metap"
	}
	tenantID := uuid.New()
	dsnSecretRef := "METAP_TEST_DEDICATED_DSN_" + strings.ReplaceAll(tenantID.String(), "-", "")
	t.Setenv(dsnSecretRef, databaseURL)
	insertDedicatedTenant(t, pool, tenantID, dsnSecretRef)
	defer cleanupTenant(pool, tenantID)

	router := testRouter(pool)
	tx, err := router.Begin(context.Background(), tenantID)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	var one int
	if err := tx.QueryRow(context.Background(), "SELECT 1").Scan(&one); err != nil {
		t.Fatal(err)
	}
	if one != 1 {
		t.Fatalf("got %d", one)
	}
}

func TestDedicatedDbTenantWithMissingSecretFailsClearly(t *testing.T) {
	pool := testPool(t)
	tenantID := uuid.New()
	insertDedicatedTenant(t, pool, tenantID, "METAP_TEST_DEDICATED_DSN_NEVER_SET")
	defer cleanupTenant(pool, tenantID)

	router := testRouter(pool)
	_, err := router.Begin(context.Background(), tenantID)
	if err == nil || !strings.Contains(err.Error(), "METAP_TEST_DEDICATED_DSN_NEVER_SET") {
		t.Fatalf("got %v", err)
	}
}

// Regression test for audit 06 finding #1: PoolFor used to validate a Schema tenant's
// schema_name and then return the shared pool anyway. Writes through Begin and reads back,
// unqualified, through PoolFor: the two must agree. Also asserts the shared pool still does not
// see the row, so this is proving real schema scoping rather than everything having quietly
// landed in one shared table.
func TestPoolForResolvesTheSameSchemaBeginDoes(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tenantID := uuid.New()
	schema := "t_poolfor" + tenantID.String()[:8]
	insertTenant(t, pool, tenantID, "schema", &schema, "active")
	defer cleanupTenant(pool, tenantID)
	if _, err := pool.Exec(ctx, `CREATE SCHEMA IF NOT EXISTS "`+schema+`"`); err != nil {
		t.Fatal(err)
	}
	defer pool.Exec(context.Background(), `DROP SCHEMA "`+schema+`" CASCADE`)
	if _, err := pool.Exec(ctx, `CREATE TABLE "`+schema+`".policies (LIKE metadata.policies INCLUDING DEFAULTS INCLUDING CONSTRAINTS)`); err != nil {
		t.Fatal(err)
	}

	router := testRouter(pool)

	tx, err := router.Begin(ctx, tenantID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx,
		"INSERT INTO policies (id, tenant_id, entity, action, effect) VALUES (gen_random_uuid(), $1, 'audit06', 'read', 'allow')",
		tenantID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	tenantPool, err := router.PoolFor(ctx, tenantID)
	if err != nil {
		t.Fatal(err)
	}
	var seen int64
	if err := tenantPool.QueryRow(ctx, "SELECT count(*) FROM policies WHERE entity = 'audit06'").Scan(&seen); err != nil {
		t.Fatal(err)
	}
	if seen != 1 {
		t.Fatalf("PoolFor must resolve the same schema Begin wrote to — got %d, meaning it is still "+
			"handing back the shared pool with the default search_path", seen)
	}

	var sharedSeen int64
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM policies WHERE entity = 'audit06'").Scan(&sharedSeen); err != nil {
		t.Fatal(err)
	}
	if sharedSeen != 0 {
		t.Fatal("the row must live in the tenant's own schema, not the shared metadata.policies")
	}
}
