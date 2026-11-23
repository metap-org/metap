package scheduler

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/metap-org/metap/pkg/control"
	"github.com/metap-org/metap/pkg/kit/standard"
)

// RunTenantPurge marks tenants whose deletion retention has run out as deleted, once at start
// and then every interval, until ctx is canceled. It changes the registry state only (the router
// refuses a deleted tenant for good); removing a tenant's data is a separate, deliberate step.
func RunTenantPurge(ctx context.Context, pool *pgxpool.Pool, interval time.Duration) {
	registry := control.NewPostgresTenantRegistry(pool)
	slog.Info("tenant deletion sweep running", "interval_secs", int(interval.Seconds()))
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		purgeTenantsOnce(ctx, registry)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func purgeTenantsOnce(ctx context.Context, registry *control.PostgresTenantRegistry) {
	ids, err := registry.PurgeDue(ctx, time.Now())
	if err != nil {
		if ctx.Err() == nil {
			slog.Error("tenant deletion sweep failed", standard.LogError, err)
		}
		return
	}
	for _, id := range ids {
		slog.Info("tenant deleted: retention ran out", "tenant_id", id)
	}
}
