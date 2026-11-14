// Package notificationworker is an entity-agnostic notification consumer: logs every workflow
// transition event. The first real consumer of EventBus.Subscribe. Deliberately minimal (stdout
// logging, not email/SMS/webhook) — the point of this package is the consume-loop plumbing, which
// any real notification channel would reuse.
package notificationworker

import (
	"context"
	"github.com/metap-org/metap/pkg/events"
	"log/slog"

	"github.com/metap-org/metap/pkg/infra/eventbus"
)

const Queue = "notification.workflow-transitioned"

// RoutingKey binds every entity's transition topic regardless of how domain-namespaced its name is.
const RoutingKey = events.AnyTransition

// Run runs the consume loop until ctx is canceled — a thin wrapper around
// eventbus.RunResilientConsumer supplying the notification-specific handler (notify, always
// acks — nothing here can fail in a way worth nacking).
func Run(ctx context.Context, connect func() (eventbus.EventBus, error)) error {
	return eventbus.RunResilientConsumer(ctx, Queue, RoutingKey, nil, connect, func(ctx context.Context, event *eventbus.ConsumedEvent) {
		notify(event)
		event.AckLogged()
	})
}

func notify(event *eventbus.ConsumedEvent) {
	t := events.ParseTopic(event.RoutingKey)
	p, err := events.Decode[events.Transitioned](event.Payload)
	if err != nil {
		slog.Warn("notification: unreadable transition payload", "routing_key", event.RoutingKey, "error", err)
	}
	slog.Info("notification", "entity", t.Entity, "record_id", p.RecordID, "action", p.Action, "from", p.From, "to", p.To)
}
