//go:build integration

package tenancy

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestAUnitOfWorkCommitsEverythingThatJoinedIt(t *testing.T) {
	pool := testPool(t)
	router := testRouter(pool)
	ctx := context.Background()
	tenant := uuid.New()
	_, err := pool.Exec(ctx, "CREATE TABLE IF NOT EXISTS public.unit_probe (n int)")
	require.NoError(t, err)
	t.Cleanup(func() { pool.Exec(context.Background(), "DROP TABLE IF EXISTS public.unit_probe") })

	uctx, unit, err := router.BeginUnit(ctx, tenant)
	require.NoError(t, err)

	// Two separate "transactions" opened through the router, as the CRUD service does, both join
	// the unit; each is a savepoint, so committing one does not commit the unit.
	for n := 1; n <= 2; n++ {
		tx, err := router.Begin(uctx, tenant)
		require.NoError(t, err)
		_, err = tx.Exec(uctx, "INSERT INTO public.unit_probe VALUES ($1)", n)
		require.NoError(t, err)
		require.NoError(t, tx.Commit(uctx))
	}
	var seenOutside int
	require.NoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM public.unit_probe").Scan(&seenOutside))
	require.Equal(t, 0, seenOutside, "nothing is visible outside until the unit commits")

	require.NoError(t, unit.Commit(ctx))
	require.NoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM public.unit_probe").Scan(&seenOutside))
	require.Equal(t, 2, seenOutside)
}

func TestAUnitOfWorkRollsBackEverythingThatJoinedIt(t *testing.T) {
	pool := testPool(t)
	router := testRouter(pool)
	ctx := context.Background()
	tenant := uuid.New()
	_, err := pool.Exec(ctx, "CREATE TABLE IF NOT EXISTS public.unit_probe (n int)")
	require.NoError(t, err)
	t.Cleanup(func() { pool.Exec(context.Background(), "DROP TABLE IF EXISTS public.unit_probe") })

	uctx, unit, err := router.BeginUnit(ctx, tenant)
	require.NoError(t, err)
	tx, err := router.Begin(uctx, tenant)
	require.NoError(t, err)
	_, err = tx.Exec(uctx, "INSERT INTO public.unit_probe VALUES (1)")
	require.NoError(t, err)
	require.NoError(t, tx.Commit(uctx), "the joined part committed...")

	unit.Rollback(ctx)
	var n int
	require.NoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM public.unit_probe").Scan(&n))
	require.Equal(t, 0, n, "...but the unit rolling back takes it with it")
}

func TestAJoinedPartThatRollsBackLeavesTheRestOfTheUnit(t *testing.T) {
	pool := testPool(t)
	router := testRouter(pool)
	ctx := context.Background()
	tenant := uuid.New()
	_, err := pool.Exec(ctx, "CREATE TABLE IF NOT EXISTS public.unit_probe (n int)")
	require.NoError(t, err)
	t.Cleanup(func() { pool.Exec(context.Background(), "DROP TABLE IF EXISTS public.unit_probe") })

	uctx, unit, err := router.BeginUnit(ctx, tenant)
	require.NoError(t, err)
	keep, _ := router.Begin(uctx, tenant)
	_, _ = keep.Exec(uctx, "INSERT INTO public.unit_probe VALUES (1)")
	require.NoError(t, keep.Commit(uctx))
	drop, _ := router.Begin(uctx, tenant)
	_, _ = drop.Exec(uctx, "INSERT INTO public.unit_probe VALUES (2)")
	require.NoError(t, drop.Rollback(uctx))
	require.NoError(t, unit.Commit(ctx))

	var n int
	require.NoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM public.unit_probe").Scan(&n))
	require.Equal(t, 1, n, "a failed operation inside the unit undoes only itself")
}

func TestOnCommitRunsAfterTheCommitAndNeverAfterARollback(t *testing.T) {
	pool := testPool(t)
	router := testRouter(pool)
	ctx := context.Background()
	tenant := uuid.New()

	ran := 0
	uctx, unit, err := router.BeginUnit(ctx, tenant)
	require.NoError(t, err)
	OnCommit(uctx, func() { ran++ })
	require.Equal(t, 0, ran, "not before the commit")
	require.NoError(t, unit.Commit(ctx))
	require.Equal(t, 1, ran)

	uctx, unit, err = router.BeginUnit(ctx, tenant)
	require.NoError(t, err)
	OnCommit(uctx, func() { ran++ })
	unit.Rollback(ctx)
	require.Equal(t, 1, ran, "a rolled-back unit never runs it")

	OnCommit(ctx, func() { ran++ })
	require.Equal(t, 2, ran, "with no unit it runs now")
}

func TestUnitsDoNotNest(t *testing.T) {
	pool := testPool(t)
	router := testRouter(pool)
	ctx := context.Background()
	tenant := uuid.New()
	uctx, unit, err := router.BeginUnit(ctx, tenant)
	require.NoError(t, err)
	defer unit.Rollback(ctx)
	_, _, err = router.BeginUnit(uctx, tenant)
	require.ErrorIs(t, err, ErrUnitOpen)
}

func TestAUnitOnlyCapturesItsOwnTenant(t *testing.T) {
	pool := testPool(t)
	router := testRouter(pool)
	ctx := context.Background()
	mine, other := uuid.New(), uuid.New()
	uctx, unit, err := router.BeginUnit(ctx, mine)
	require.NoError(t, err)
	defer unit.Rollback(ctx)

	// Another tenant's work inside the same context gets a connection of its own.
	deadline, cancel := context.WithTimeout(uctx, 5*time.Second)
	defer cancel()
	tx, err := router.Begin(deadline, other)
	require.NoError(t, err)
	require.NoError(t, tx.Rollback(deadline))
}

// The point of a unit of work: everything that joins it runs on the unit's own database connection,
// so a pool of one serves it (no path holds a connection while it waits for another).
func TestEverythingInAUnitRunsOnOneConnection(t *testing.T) {
	pool := testPool(t)
	router := testRouter(pool)
	ctx := context.Background()
	tenant := uuid.New()

	uctx, unit, err := router.BeginUnit(ctx, tenant)
	require.NoError(t, err)
	defer unit.Rollback(ctx)

	var unitPID int
	require.NoError(t, unit.Tx().QueryRow(uctx, "SELECT pg_backend_pid()").Scan(&unitPID))
	for range 3 {
		tx, err := router.Begin(uctx, tenant)
		require.NoError(t, err)
		var pid int
		require.NoError(t, tx.QueryRow(uctx, "SELECT pg_backend_pid()").Scan(&pid))
		require.NoError(t, tx.Commit(uctx))
		require.Equal(t, unitPID, pid, "a transaction opened inside the unit is on the unit's connection")
	}
}
