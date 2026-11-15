// notification-worker logs every workflow transition event.
package main

import (
	"context"
	"github.com/metap-org/metap/pkg/kit/shutdown"
	"log/slog"
	"os"

	"github.com/metap-org/metap/pkg/infra"
	"github.com/metap-org/metap/pkg/infra/eventbus"
	"github.com/metap-org/metap/pkg/kit/standard"
	"github.com/metap-org/metap/pkg/notificationworker"
)

func main() {
	infra.InitTracing()
	config, err := infra.LoadConfig()
	if err != nil {
		slog.Error("loading config", standard.LogError, err)
		os.Exit(1)
	}

	slog.Info("ready, listening", standard.LogQueue, notificationworker.Queue, standard.LogRoutingKey, notificationworker.RoutingKey)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		shutdown.Wait()
		cancel()
	}()
	defer cancel()

	connect := func() (eventbus.EventBus, error) { return eventbus.ConnectRabbitEventBus(config.RabbitMQURL) }
	if err := notificationworker.Run(ctx, connect); err != nil {
		slog.Error("notification worker exited with an error", standard.LogError, err)
		os.Exit(1)
	}
}
