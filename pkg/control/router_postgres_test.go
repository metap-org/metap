//go:build integration

package control

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/metap-org/metap/pkg/testkit"
	"github.com/stretchr/testify/require"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := testkit.NewPostgres(t, testkit.WithSearchPath("metadata", "public"))
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
	require.NoError(t, err)
}

func insertDedicatedTenant(t *testing.T, pool *pgxpool.Pool, id uuid.UUID, dsnSecretRef string) {
	_, err := pool.Exec(context.Background(),
		"INSERT INTO control.tenants (id, tier, strategy, dsn_secret_ref, status) VALUES ($1, 'paid', 'dedicated_db', $2, 'active')",
		id, dsnSecretRef)
	require.NoError(t, err)
}

func cleanupTenant(pool *pgxpool.Pool, id uuid.UUID) {
	pool.Exec(context.Background(), "DELETE FROM control.tenants WHERE id = $1", id)
}

func TestUnregisteredTenantFallsBackToPublicSchema(t *testing.T) {
	pool := testPool(t)
	router := testRouter(pool)
	tenantID := uuid.New() // never inserted into control.tenants

	tx, err := router.Begin(context.Background(), tenantID)
	require.NoError(t, err)
	defer tx.Rollback(context.Background())
	require.Contains(t, searchPath(context.Background(), tx), "public", "expected the public schema in search_path")
}

func TestRegisteredSchemaTenantRoutesToItsSchemaAndDoesNotLeak(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tenantID := uuid.New()
	schemaName := "t_test1"
	insertTenant(t, pool, tenantID, "schema", &schemaName, "active")
	defer cleanupTenant(pool, tenantID)
	if _, err := pool.Exec(ctx, "CREATE SCHEMA IF NOT EXISTS t_test1"); err != nil {
		require.FailNow(t, fmt.Sprint(err))
	}
	defer pool.Exec(context.Background(), "DROP SCHEMA t_test1")

	router := testRouter(pool)
	tx, err := router.Begin(ctx, tenantID)
	require.NoError(t, err)
	require.Contains(t, searchPath(ctx, tx), "t_test1", "expected t_test1 in search_path")
	require.NoError(t, tx.Commit(ctx))

	// A transaction opened directly on the same pool, bypassing Router, must NOT see the
	// previous transaction's SET LOCAL search_path — proves it was transaction-scoped, not
	// leaked onto the pooled physical connection.
	plainTx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer plainTx.Rollback(ctx)
	require.NotContains(t, searchPath(ctx, plainTx), "t_test1", "search_path leaked across transactions on the same pooled connection")
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
	require.True(t, ok, "expected ErrTenantSuspended, got %v", err)
	require.Equal(t, ErrTenantSuspended, re.Kind, "expected ErrTenantSuspended, got %v", err)
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
	require.True(t, ok, "expected ErrTenantMigrating, got %v", err)
	require.Equal(t, ErrTenantMigrating, re.Kind, "expected ErrTenantMigrating, got %v", err)
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
	require.True(t, ok, "expected ErrInvalidSchemaName, got %v", err)
	require.Equal(t, ErrInvalidSchemaName, re.Kind, "expected ErrInvalidSchemaName, got %v", err)
}

// dsnSecretRef points at an env var holding a genuinely separate database (testkit gives each
// call its own) — exercises the full path: EnvStore lookup -> new pool -> Begin on that DB.
func TestDedicatedDbTenantRoutesThroughSecretStoreToItsOwnPool(t *testing.T) {
	pool := testPool(t)
	databaseURL := testkit.NewDatabaseURL(t)
	tenantID := uuid.New()
	dsnSecretRef := "METAP_TEST_DEDICATED_DSN_" + strings.ReplaceAll(tenantID.String(), "-", "")
	t.Setenv(dsnSecretRef, databaseURL)
	insertDedicatedTenant(t, pool, tenantID, dsnSecretRef)
	defer cleanupTenant(pool, tenantID)

	router := testRouter(pool)
	tx, err := router.Begin(context.Background(), tenantID)
	require.NoError(t, err)
	defer tx.Rollback(context.Background())
	var one int
	require.NoError(t, tx.QueryRow(context.Background(), "SELECT 1").Scan(&one))
	require.Equal(t, 1, one, "got %d", one)
	var dbName string
	require.NoError(t, tx.QueryRow(context.Background(), "SELECT current_database()").Scan(&dbName))
	var sharedName string
	require.NoError(t, pool.QueryRow(context.Background(), "SELECT current_database()").Scan(&sharedName))
	require.NotEqual(t, sharedName, dbName, "dedicated tenant's transaction ran on the shared database %q", sharedName)
}

func TestDedicatedDbTenantWithMissingSecretFailsClearly(t *testing.T) {
	pool := testPool(t)
	tenantID := uuid.New()
	insertDedicatedTenant(t, pool, tenantID, "METAP_TEST_DEDICATED_DSN_NEVER_SET")
	defer cleanupTenant(pool, tenantID)

	router := testRouter(pool)
	_, err := router.Begin(context.Background(), tenantID)
	require.Error(t, err, "got %v", err)
	require.Contains(t, err.Error(), "METAP_TEST_DEDICATED_DSN_NEVER_SET", "got %v", err)
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
		require.FailNow(t, fmt.Sprint(err))
	}
	defer pool.Exec(context.Background(), `DROP SCHEMA "`+schema+`" CASCADE`)
	if _, err := pool.Exec(ctx, `CREATE TABLE "`+schema+`".policies (LIKE metadata.policies INCLUDING DEFAULTS INCLUDING CONSTRAINTS)`); err != nil {
		require.FailNow(t, fmt.Sprint(err))
	}

	router := testRouter(pool)

	tx, err := router.Begin(ctx, tenantID)
	require.NoError(t, err)
	_, err = tx.Exec(ctx,
		"INSERT INTO policies (id, tenant_id, entity, action, effect) VALUES (gen_random_uuid(), $1, 'audit06', 'read', 'allow')",
		tenantID)
	require.NoError(t, err)
	require.NoError(t, tx.Commit(ctx))

	tenantPool, err := router.PoolFor(ctx, tenantID)
	require.NoError(t, err)
	var seen int64
	require.NoError(t, tenantPool.QueryRow(ctx, "SELECT count(*) FROM policies WHERE entity = 'audit06'").Scan(&seen))
	require.EqualValues(t, 1, seen, "PoolFor must resolve the same schema Begin wrote to — got %d, meaning it is still "+
		"handing back the shared pool with the default search_path", seen)

	var sharedSeen int64
	require.NoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM policies WHERE entity = 'audit06'").Scan(&sharedSeen))
	require.EqualValues(t, 0, sharedSeen, "the row must live in the tenant's own schema, not the shared metadata.policies")
}
