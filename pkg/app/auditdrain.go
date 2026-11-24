package app

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/metap-org/metap/pkg/audit"
	"github.com/metap-org/metap/pkg/kit/env"
	"github.com/metap-org/metap/pkg/kit/standard"
	"github.com/metap-org/metap/pkg/tenancy"
)

// tenantListTTL is how long the list of tenants with a dedicated database is reused between polls.
const tenantListTTL = 30 * time.Second

// startAuditDrain runs the audit drain in the background until ctx ends. A write puts its audit
// entry in the outbox in its own transaction; this loop moves the entries into each tenant's hash
// chain (a second or less later). It drains the shared database and the dedicated database of every
// tenant that has one, since an entry lives where the write it describes was made.
//
// AUDIT_DRAIN_DISABLED=true turns it off in a process when another one (cmd/outbox-publisher)
// drains instead. Running several is harmless: writing an entry is idempotent.
func startAuditDrain(ctx context.Context, shared *pgxpool.Pool, router *tenancy.Router, store *audit.PostgresStore) {
	if env.Bool("AUDIT_DRAIN_DISABLED") {
		return
	}
	poll := env.Millis("AUDIT_DRAIN_POLL_MS", 500*time.Millisecond)
	batch := env.Int("AUDIT_DRAIN_BATCH_SIZE", 100)
	registry := tenancy.NewPostgresTenantRegistry(shared)
	go audit.RunDrain(ctx, DrainPools(shared, router, registry), store, poll, batch)
}

// DrainPools lists the databases whose outbox holds audit entries: the shared one, and the dedicated
// database of each tenant that has one. A tenant whose database cannot be opened right now is
// skipped (logged) and tried again on the next listing.
func DrainPools(shared *pgxpool.Pool, router *tenancy.Router, registry *tenancy.PostgresTenantRegistry) func(context.Context) ([]*pgxpool.Pool, error) {
	var (
		mu      sync.Mutex
		fetched time.Time
		tenants []tenancy.TenantSummary
	)
	return func(ctx context.Context) ([]*pgxpool.Pool, error) {
		mu.Lock()
		if time.Since(fetched) > tenantListTTL {
			list, err := registry.List(ctx)
			if err != nil {
				mu.Unlock()
				return []*pgxpool.Pool{shared}, err
			}
			tenants, fetched = list, time.Now()
		}
		current := tenants
		mu.Unlock()

		pools := []*pgxpool.Pool{shared}
		for _, t := range current {
			if t.Strategy.Kind != tenancy.StrategyDedicatedDb {
				continue
			}
			p, err := router.PoolFor(ctx, t.ID)
			if err != nil {
				slog.Warn("cannot open a tenant's database to drain audit entries", "tenant_id", t.ID, standard.LogError, err)
				continue
			}
			pools = append(pools, p)
		}
		return pools, nil
	}
}
