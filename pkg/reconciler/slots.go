package reconciler

import (
	"context"
	"fmt"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"
)

// reconcileSlots limits, per pool, how many reconciles run at once (see acquireReconcileSlot).
var reconcileSlots sync.Map // *pgxpool.Pool -> chan struct{}

// acquireReconcileSlot reserves room in pool for one reconcile, which needs two connections at
// once (the advisory lock's, held to the end, and one for each step). A pool of two or more serves
// MaxConns/2 reconciles at a time and the rest wait here; a pool of one cannot serve any.
func acquireReconcileSlot(ctx context.Context, pool *pgxpool.Pool) (func(), error) {
	max := int(pool.Config().MaxConns)
	if max < 2 {
		return nil, fmt.Errorf("reconcile needs a connection pool of at least 2 (one holds the lock, one runs the steps), this one has %d", max)
	}
	v, _ := reconcileSlots.LoadOrStore(pool, make(chan struct{}, max/2))
	slots := v.(chan struct{})
	select {
	case slots <- struct{}{}:
		return func() { <-slots }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
