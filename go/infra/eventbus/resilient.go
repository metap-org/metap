package eventbus

import (
	"context"
	"log/slog"
	"time"
)

// BackoffDelay: exponential, capped at 30s (1s, 2s, 4s, 8s, 16s, 30s, 30s, ...) — the reconnect
// schedule after a connection loss.
func BackoffDelay(attempt uint32) time.Duration {
	if attempt > 4 {
		attempt = 4
	}
	d := time.Duration(1<<attempt) * time.Second
	if d > 30*time.Second {
		d = 30 * time.Second
	}
	return d
}

// SleepOrShutdown returns false if ctx is canceled first — the caller should stop retrying and
// exit rather than sleeping through a requested shutdown.
func SleepOrShutdown(ctx context.Context, delay time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(delay):
		return true
	}
}

// Handler is called for each delivered event; it is responsible for acking/nacking it itself.
type Handler func(ctx context.Context, event *ConsumedEvent)

// RunResilientConsumer runs handle for every event on (queue, routingKey) until ctx is
// canceled, reconnecting with backoff on any connection loss instead of requiring a full
// process restart. connect is called again for every (re)connection attempt.
func RunResilientConsumer(ctx context.Context, queue, routingKey string, retryPolicy *RetryPolicy,
	connect func() (EventBus, error), handle Handler) error {
	var attempt uint32

reconnect:
	for {
		if ctx.Err() != nil {
			return nil
		}
		bus, err := connect()
		if err != nil {
			slog.Warn("failed to connect to event bus, retrying", "error", err, "attempt", attempt, "queue", queue)
			if !SleepOrShutdown(ctx, BackoffDelay(attempt)) {
				return nil
			}
			attempt++
			continue reconnect
		}

		events, err := bus.Subscribe(ctx, queue, routingKey, retryPolicy)
		if err != nil {
			slog.Warn("failed to subscribe, reconnecting", "error", err, "attempt", attempt, "queue", queue)
			_ = bus.Close()
			if !SleepOrShutdown(ctx, BackoffDelay(attempt)) {
				return nil
			}
			attempt++
			continue reconnect
		}
		if attempt > 0 {
			slog.Info("reconnected to event bus", "attempt", attempt, "queue", queue)
		}
		attempt = 0

		for {
			select {
			case <-ctx.Done():
				slog.Info("shutdown signal received, exiting", "queue", queue)
				_ = bus.Close()
				return nil
			case event, ok := <-events:
				if !ok {
					slog.Warn("event stream closed unexpectedly (bus disconnected?), reconnecting", "queue", queue)
					_ = bus.Close()
					continue reconnect
				}
				handle(ctx, event)
			}
		}
	}
}
