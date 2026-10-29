// notification-worker logs every workflow transition event — ported from metap's Rust
// metap-notification-worker binary.
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/metap-org/metap-go/infra"
	"github.com/metap-org/metap-go/infra/eventbus"
	"github.com/metap-org/metap-go/notificationworker"
	"github.com/metap-org/metap-go/runtime"
)

func main() {
	infra.InitTracing()
	config, err := infra.LoadConfig()
	if err != nil {
		slog.Error("loading config", "error", err)
		os.Exit(1)
	}

	slog.Info("ready, listening", "queue", notificationworker.Queue, "routing_key", notificationworker.RoutingKey)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		runtime.WaitForShutdownSignal()
		cancel()
	}()
	defer cancel()

	connect := func() (eventbus.EventBus, error) { return eventbus.ConnectRabbitEventBus(config.RabbitMQURL) }
	if err := notificationworker.Run(ctx, connect); err != nil {
		slog.Error("notification worker exited with an error", "error", err)
		os.Exit(1)
	}
}
