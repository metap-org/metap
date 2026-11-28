// Keeping the claim on a run alive while it is being run.
package scheduler

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/metap-org/metap/pkg/jobs/cron"
	"github.com/metap-org/metap/pkg/kit/standard"
)

const (
	// defaultRunLease is how long a claim lasts without being renewed, so how long it takes for the
	// run of a dead worker to become claimable.
	defaultRunLease = time.Minute
	// leaseRenewals is how many times a lease is renewed within its own length.
	leaseRenewals = 3
	// recordTimeout bounds writing a result, which goes on after ctx is canceled by a shutdown.
	recordTimeout = 10 * time.Second
)

func (c *ExecutorConfig) runLease() time.Duration {
	if c.RunLease > 0 {
		return c.RunLease
	}
	return defaultRunLease
}

// holdLease renews the claim until stop is called. The returned context is canceled if the claim is
// lost (someone else took the run, or it could not be renewed for a whole lease), so the job stops
// rather than run beside its successor. stop reports whether that happened.
func holdLease(ctx context.Context, pool *pgxpool.Pool, runID, token uuid.UUID, ttl time.Duration) (context.Context, func() bool) {
	runCtx, cancel := context.WithCancel(ctx)
	done, result := make(chan struct{}), make(chan bool, 1)
	// A shutdown cancels the job, not the claim: the result is still to be recorded.
	renewCtx := context.WithoutCancel(ctx)
	go func() {
		every := ttl / leaseRenewals
		timer := time.NewTicker(every)
		defer timer.Stop()
		renewed := time.Now()
		for {
			select {
			case <-done:
				result <- false
				return
			case <-timer.C:
			}
			attempt, end := context.WithTimeout(renewCtx, every)
			held, err := cron.RenewRunLease(attempt, pool, runID, token, ttl)
			end()
			switch {
			case err == nil && held:
				renewed = time.Now()
				continue
			case err != nil && time.Since(renewed) < ttl:
				slog.Warn("failed to renew cron job run lease, trying again", standard.LogRunID, runID, standard.LogError, err)
				continue
			}
			slog.Error("lost the lease on a cron job run, stopping it", standard.LogRunID, runID, standard.LogError, err)
			cancel()
			<-done
			result <- true
			return
		}
	}()
	return runCtx, func() bool {
		close(done)
		lost := <-result
		cancel()
		return lost
	}
}
