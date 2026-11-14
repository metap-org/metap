// notification-worker logs every workflow transition event.
package main

import (
	"context"
	"github.com/metap-org/metap/pkg/kit/shutdown"
	"log/slog"
	"os"

	"github.com/metap-org/metap/pkg/infra"
	"github.com/metap-org/metap/pkg/infra/eventbus"
	"github.com/metap-org/metap/pkg/notificationworker"
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
		shutdown.Wait()
		cancel()
	}()
	defer cancel()

	connect := func() (eventbus.EventBus, error) { return eventbus.ConnectRabbitEventBus(config.RabbitMQURL) }
	if err := notificationworker.Run(ctx, connect); err != nil {
		slog.Error("notification worker exited with an error", "error", err)
		os.Exit(1)
	}
}
