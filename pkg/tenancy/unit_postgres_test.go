//go:build integration

package tenancy

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// probeCount counts the rows of the probe table as someone outside the unit sees them. It uses a
// connection of its own, not the pool: while a unit is open it holds the pool's only connection
// when the suite runs with a pool of one, and "outside" must not wait for it.
func probeCount(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	conn, err := pgx.ConnectConfig(context.Background(), pool.Config().ConnConfig.Copy())
	require.NoError(t, err)
	defer conn.Close(context.Background())
	var n int
	require.NoError(t, conn.QueryRow(context.Background(), "SELECT count(*) FROM public.unit_probe").Scan(&n))
	return n
}

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
	require.Equal(t, 0, probeCount(t, pool), "nothing is visible outside until the unit commits")

	require.NoError(t, unit.Commit(ctx))
	require.Equal(t, 2, probeCount(t, pool))
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

// Callers with the same key never run their critical sections at the same time, however many there are.
func TestWithKeyLockSerialisesCallersWithTheSameKey(t *testing.T) {
	pool := testPool(t)
	router := testRouter(pool)
	ctx := context.Background()
	tenant := uuid.New()
	_, err := pool.Exec(ctx, "CREATE TABLE IF NOT EXISTS public.lock_probe (n int)")
	require.NoError(t, err)
	t.Cleanup(func() { pool.Exec(context.Background(), "DROP TABLE IF EXISTS public.lock_probe") })

	// count-then-insert under a limit of 3, attempted by 12 callers at once: without the lock they all
	// read the same count and overshoot.
	const limit, callers = 3, 12
	var wg sync.WaitGroup
	var inserted atomic.Int32
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = router.WithKeyLock(ctx, tenant, tenant.String()+"|probe", func(ctx context.Context) error {
				tx, err := router.Begin(ctx, tenant) // joins the lock's transaction
				if err != nil {
					return err
				}
				var n int
				if err := tx.QueryRow(ctx, "SELECT count(*) FROM public.lock_probe").Scan(&n); err != nil {
					_ = tx.Rollback(ctx)
					return err
				}
				time.Sleep(15 * time.Millisecond) // widen the window a race would need
				if n >= limit {
					return tx.Commit(ctx)
				}
				if _, err := tx.Exec(ctx, "INSERT INTO public.lock_probe VALUES (1)"); err != nil {
					_ = tx.Rollback(ctx)
					return err
				}
				inserted.Add(1)
				return tx.Commit(ctx)
			})
		}()
	}
	wg.Wait()
	var total int
	require.NoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM public.lock_probe").Scan(&total))
	require.Equal(t, limit, total, "the limit holds: no overshoot")
	require.EqualValues(t, limit, inserted.Load())
}

func TestWithKeyLockInsideAUnitHoldsTheLockUntilTheUnitEnds(t *testing.T) {
	pool := testPool(t)
	router := testRouter(pool)
	ctx := context.Background()
	tenant := uuid.New()

	uctx, unit, err := router.BeginUnit(ctx, tenant)
	require.NoError(t, err)
	require.NoError(t, router.WithKeyLock(uctx, tenant, "k", func(context.Context) error { return nil }))

	// Another caller with the same key waits while the unit is open...
	got := make(chan struct{})
	go func() {
		_ = router.WithKeyLock(ctx, tenant, "k", func(context.Context) error { close(got); return nil })
	}()
	select {
	case <-got:
		t.Fatal("the lock was released when WithKeyLock returned, not when the unit ended")
	case <-time.After(300 * time.Millisecond):
	}
	require.NoError(t, unit.Commit(ctx))
	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("the waiting caller did not get the lock after the unit committed")
	}
}

func TestWithKeyLockRollsBackWhenFnFails(t *testing.T) {
	pool := testPool(t)
	router := testRouter(pool)
	ctx := context.Background()
	tenant := uuid.New()
	_, err := pool.Exec(ctx, "CREATE TABLE IF NOT EXISTS public.lock_probe2 (n int)")
	require.NoError(t, err)
	t.Cleanup(func() { pool.Exec(context.Background(), "DROP TABLE IF EXISTS public.lock_probe2") })

	boom := errors.New("fn failed")
	err = router.WithKeyLock(ctx, tenant, "k2", func(ctx context.Context) error {
		tx, _ := router.Begin(ctx, tenant)
		_, _ = tx.Exec(ctx, "INSERT INTO public.lock_probe2 VALUES (1)")
		_ = tx.Commit(ctx)
		return boom
	})
	require.ErrorIs(t, err, boom)
	var n int
	require.NoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM public.lock_probe2").Scan(&n))
	require.Equal(t, 0, n, "a failed critical section leaves nothing behind")
}
