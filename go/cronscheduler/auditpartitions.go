// Keeps metadata.audit_trail_entries' month partitions created ahead of time (migration 0038),
// alongside the cron loops — same reason RunOAuthCleanup lives here: this is the one always-on
// housekeeping process. Rows never fail to insert without it (the default partition catches
// them); this only keeps each month in its own partition so old months can be detached and
// archived as a unit. Nothing here deletes audit history.
package cronscheduler

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/metap-org/metap-go/audit"
)

// RunAuditPartitionMaintenance runs audit.EnsurePartitions once at start and then every
// interval until ctx is canceled. Stays idle if migration 0038 is not applied.
func RunAuditPartitionMaintenance(ctx context.Context, pool *pgxpool.Pool, interval time.Duration, monthsAhead int) {
	present, err := audit.PartitioningPresent(ctx, pool)
	switch {
	case err != nil:
		slog.Warn("could not check for partitioned audit table — partition maintenance disabled", "error", err)
		<-ctx.Done()
		return
	case !present:
		slog.Info("partitioned audit table not present (migration 0038 not applied) — partition maintenance disabled")
		<-ctx.Done()
		return
	}

	slog.Info("audit partition maintenance running", "interval_secs", int(interval.Seconds()), "months_ahead", monthsAhead)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if created, err := audit.EnsurePartitions(ctx, pool, monthsAhead); err != nil {
			slog.Error("audit partition maintenance failed", "error", err)
		} else if created > 0 {
			slog.Info("audit partitions created", "count", created)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
