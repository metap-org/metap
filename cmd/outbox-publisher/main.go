// outbox-publisher drains metadata.outbox_events and publishes each row to RabbitMQ.
package main

import (
	"context"
	"github.com/metap-org/metap/migrations"
	dbmigrate "github.com/metap-org/metap/pkg/db/migrate"
	"github.com/metap-org/metap/pkg/kit/shutdown"
	"log/slog"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/metap-org/metap/pkg/audit"
	"github.com/metap-org/metap/pkg/config"
	"github.com/metap-org/metap/pkg/kit/env"
	"github.com/metap-org/metap/pkg/kit/standard"
	"github.com/metap-org/metap/pkg/messaging/eventbus"
	"github.com/metap-org/metap/pkg/messaging/outbox"
)

func main() {
	config.InitLogging()
	config, err := config.LoadApp()
	if err != nil {
		slog.Error("loading config", standard.LogError, err)
		os.Exit(1)
	}

	pollMs := uint64(env.Int("OUTBOX_POLL_MS", 1000))
	batchSize := int64(env.Int("OUTBOX_BATCH_SIZE", 100))

	slog.Info("connecting to postgres...")
	ctx, cancel := signalContext()
	defer cancel()

	pool, err := pgxpool.New(ctx, config.OutboxDatabaseURLOrDefault())
	if err != nil {
		slog.Error("connecting to postgres", standard.LogError, err)
		os.Exit(1)
	}
	defer pool.Close()
	if err := dbmigrate.Require(ctx, pool, migrations.FS); err != nil {
		slog.Error("the database is not migrated", standard.LogError, err)
		os.Exit(1)
	}

	// The same loop also moves audit entries from this outbox into the chain (they are never sent to
	// the broker). The chain lives in the main database, which may not be the one this outbox is in.
	if !env.Bool("AUDIT_DRAIN_DISABLED") {
		chainPool, err := pgxpool.New(ctx, config.DatabaseURL)
		if err != nil {
			slog.Error("connecting to postgres for the audit chain", standard.LogError, err)
			os.Exit(1)
		}
		defer chainPool.Close()
		drain := func(context.Context) ([]*pgxpool.Pool, error) { return []*pgxpool.Pool{pool}, nil }
		go audit.RunDrain(ctx, drain, audit.NewPostgresStore(chainPool), env.Millis("AUDIT_DRAIN_POLL_MS", 500*time.Millisecond), env.Int("AUDIT_DRAIN_BATCH_SIZE", 100))
	}

	slog.Info("ready, polling", "poll_ms", pollMs, "batch_size", batchSize)

	connect := func() (eventbus.EventBus, error) { return eventbus.ConnectRabbitEventBus(config.RabbitMQURL) }
	if err := outbox.Run(ctx, pool, connect, pollMs, batchSize); err != nil {
		slog.Error("outbox publisher exited with an error", standard.LogError, err)
		os.Exit(1)
	}
}

func signalContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		shutdown.Wait()
		cancel()
	}()
	return ctx, cancel
}
