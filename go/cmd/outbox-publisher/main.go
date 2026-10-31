// outbox-publisher drains metadata.outbox_events and publishes each row to RabbitMQ — ported
// from metap's Rust metap-outbox-publisher binary.
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/metap-org/metap-go/infra"
	"github.com/metap-org/metap-go/infra/eventbus"
	"github.com/metap-org/metap-go/outboxpublisher"
	"github.com/metap-org/metap-go/runtime"
)

func main() {
	infra.InitTracing()
	config, err := infra.LoadConfig()
	if err != nil {
		slog.Error("loading config", "error", err)
		os.Exit(1)
	}

	pollMs := uint64(runtime.EnvOr("OUTBOX_POLL_MS", 1000))
	batchSize := int64(runtime.EnvOr("OUTBOX_BATCH_SIZE", 100))

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
		runtime.WaitForShutdownSignal()
		cancel()
	}()
	return ctx, cancel
}
