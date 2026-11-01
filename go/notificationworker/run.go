// Package notificationworker is an entity-agnostic notification consumer: logs every workflow
// transition event — ported from metap's Rust metap-notification-worker crate. The first real
// consumer of EventBus.Subscribe. Deliberately minimal (stdout logging, not email/SMS/webhook)
// — the point of this package is the consume-loop plumbing, which any real notification channel
// would reuse.
package notificationworker

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/metap-org/metap-go/infra/eventbus"
)

const Queue = "notification.workflow-transitioned"

// RoutingKey: "#" matches any number of dot-separated words, so this binds every entity's
// transition topic regardless of how domain-namespaced its name is.
const RoutingKey = "#.workflow.transitioned"

// Run runs the consume loop until ctx is canceled — a thin wrapper around
// eventbus.RunResilientConsumer supplying the notification-specific handler (notify, always
// acks — nothing here can fail in a way worth nacking).
func Run(ctx context.Context, connect func() (eventbus.EventBus, error)) error {
	return eventbus.RunResilientConsumer(ctx, Queue, RoutingKey, nil, connect, func(ctx context.Context, event *eventbus.ConsumedEvent) {
		notify(event)
		_ = event.Ack()
	})
}

func notify(event *eventbus.ConsumedEvent) {
	entity := strings.TrimSuffix(event.RoutingKey, ".workflow.transitioned")
	var payload map[string]json.RawMessage
	_ = json.Unmarshal(event.Payload, &payload)
	recordID := jsonStringOr(payload["recordId"], "?")
	action := jsonStringOr(payload["action"], "?")
	from := jsonStringOr(payload["from"], "?")
	to := jsonStringOr(payload["to"], "?")
	slog.Info("notification", "entity", entity, "record_id", recordID, "action", action, "from", from, "to", to)
}

func jsonStringOr(raw json.RawMessage, def string) string {
	var s string
	if raw == nil || json.Unmarshal(raw, &s) != nil {
		return def
	}
	return s
}
