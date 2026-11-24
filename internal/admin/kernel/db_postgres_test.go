//go:build integration

package kernel_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/internal/admin/kernel"
	"github.com/metap-org/metap/pkg/auth/identity"
	"github.com/metap-org/metap/pkg/secrets"
	"github.com/metap-org/metap/pkg/tenancy"
	"github.com/metap-org/metap/pkg/testkit"
)

func serviceAccounts(t *testing.T, pool *pgxpool.Pool, tenant uuid.UUID) int {
	t.Helper()
	list, err := identity.ListServiceAccounts(context.Background(), pool, tenant)
	require.NoError(t, err)
	return len(list)
}

// An identity write inside a unit of work joins the unit: it commits and rolls back with it, and it
// takes no second connection, so a pool of one serves it (a store holding the pool would wait for the
// connection the unit already holds).
func TestIdentityStatementsJoinTheUnitOfWorkOnAPoolOfOne(t *testing.T) {
	pool := testkit.NewPostgres(t, testkit.WithSearchPath("metadata", "public"))
	cfg := pool.Config().Copy()
	cfg.MaxConns = 1
	small, err := pgxpool.NewWithConfig(context.Background(), cfg)
	require.NoError(t, err)
	t.Cleanup(small.Close)

	router := tenancy.NewRouter(small, tenancy.NewRegistryCache(tenancy.NewPostgresTenantRegistry(small)), secrets.EnvStore{})
	db := kernel.IdentityDB{Pool: small}
	tenant := uuid.New()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	uctx, unit, err := router.BeginUnit(ctx, tenant)
	require.NoError(t, err)
	_, err = identity.CreateServiceAccount(uctx, db, tenant, "ci", nil)
	require.NoError(t, err, "no second connection is asked of the pool the unit already holds")
	unit.Rollback(ctx)
	require.Equal(t, 0, serviceAccounts(t, pool, tenant), "rolled back with the unit")

	uctx, unit, err = router.BeginUnit(ctx, tenant)
	require.NoError(t, err)
	_, err = identity.CreateServiceAccount(uctx, db, tenant, "ci", nil)
	require.NoError(t, err)
	require.NoError(t, unit.Commit(ctx))
	require.Equal(t, 1, serviceAccounts(t, pool, tenant), "committed with the unit")
}

func TestIdentityStatementsOutsideAUnitUseThePool(t *testing.T) {
	pool := testkit.NewPostgres(t, testkit.WithSearchPath("metadata", "public"))
	tenant := uuid.New()
	_, err := identity.CreateServiceAccount(context.Background(), kernel.IdentityDB{Pool: pool}, tenant, "ci", nil)
	require.NoError(t, err)
	require.Equal(t, 1, serviceAccounts(t, pool, tenant))
}
