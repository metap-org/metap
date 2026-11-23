//go:build integration

package reconciler

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/metadata"
)

// Reconciles that arrive together on a small pool queue instead of each taking a connection for its
// lock and leaving none for the work: six at once on a pool of two all finish. (The lowcode
// orchestrator reconciles entities concurrently; before the limit this hung until the context ended.)
func TestConcurrentReconcilesOnASmallPoolQueueInsteadOfDeadlocking(t *testing.T) {
	base := reconcilerTestPool(t)
	cfg := base.Config().Copy()
	cfg.MaxConns = 2
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	const n = 6
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("test.reconciler_slots_%d", i)
		table := fmt.Sprintf("test_reconciler_slots_%d", i)
		dropReconcilerTableIfExists(t, base, table)
		t.Cleanup(func() { dropReconcilerTableIfExists(t, base, table) })
		def := testEntity(name, []metadata.EntityField{plainField("title", metadata.KindString)})
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := Reconcile(ctx, pool, uuid.New(), def, nil)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		assert.NoError(t, err)
	}
}

func TestReconcileNeedsAPoolOfAtLeastTwo(t *testing.T) {
	base := reconcilerTestPool(t)
	cfg := base.Config().Copy()
	cfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	table := "test_reconciler_slots_one"
	dropReconcilerTableIfExists(t, base, table)
	t.Cleanup(func() { dropReconcilerTableIfExists(t, base, table) })
	def := testEntity("test.reconciler_slots_one", []metadata.EntityField{plainField("title", metadata.KindString)})
	_, err = Reconcile(context.Background(), pool, uuid.New(), def, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pool of at least 2")
}
