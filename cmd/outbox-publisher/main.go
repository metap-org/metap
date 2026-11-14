// outbox-publisher drains metadata.outbox_events and publishes each row to RabbitMQ.
package main

import (
	"context"
	"github.com/metap-org/metap/pkg/kit/shutdown"
	"log/slog"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/metap-org/metap/pkg/infra"
	"github.com/metap-org/metap/pkg/infra/eventbus"
	"github.com/metap-org/metap/pkg/kit/env"
	"github.com/metap-org/metap/pkg/outboxpublisher"
)

func main() {
	infra.InitTracing()
	config, err := infra.LoadConfig()
	if err != nil {
		slog.Error("loading config", "error", err)
		os.Exit(1)
	}

	pollMs := uint64(env.Int("OUTBOX_POLL_MS", 1000))
	batchSize := int64(env.Int("OUTBOX_BATCH_SIZE", 100))

	slog.Info("connecting to postgres...")
	ctx, cancel := signalContext()
	defer cancel()

	pool, err := pgxpool.New(ctx, config.OutboxDatabaseURLOrDefault())
	if err != nil {
		slog.Error("connecting to postgres", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	slog.Info("ready, polling", "poll_ms", pollMs, "batch_size", batchSize)

	connect := func() (eventbus.EventBus, error) { return eventbus.ConnectRabbitEventBus(config.RabbitMQURL) }
	if err := outboxpublisher.Run(ctx, pool, connect, pollMs, batchSize); err != nil {
		slog.Error("outbox publisher exited with an error", "error", err)
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
