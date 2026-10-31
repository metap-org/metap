// Package eventbus is the publish/subscribe trait + RabbitMQ impl — ported from metap-infra's
// Rust event_bus module. RabbitEventBus is the only impl today; the point of the interface is
// that a second one is a new implementation, not a rewrite of every call site.
package eventbus

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

const exchange = "metap.events"

// originalRoutingKeyHeader stamps the message's true original routing key onto a retry-tier
// publish — see the Rust ORIGINAL_ROUTING_KEY_HEADER's doc comment for exactly why this is
// needed (AMQP overwrites routing_key on dead-letter republish).
const originalRoutingKeyHeader = "x-original-routing-key"

// RetryPolicy is a per-message retry-with-backoff schedule for ConsumedEvent.RetryOrGiveUp.
type RetryPolicy struct {
	Delays []time.Duration
}

// ConsumedEvent is a message received from EventBus.Subscribe, already parsed as JSON. Must be
// acked (or nacked) exactly once.
type ConsumedEvent struct {
	RoutingKey string
	Payload    json.RawMessage

	delivery     amqp.Delivery
	retryChannel *amqp.Channel
	queue        string
}

func (e *ConsumedEvent) Ack() error {
	return e.delivery.Ack(false)
}

// Nack: requeue=true for a transient failure worth retrying, false for a message that will
// never succeed.
func (e *ConsumedEvent) Nack(requeue bool) error {
	return e.delivery.Nack(false, requeue)
}

// RetryCount reads straight from the AMQP x-death header RabbitMQ itself maintains (one entry
// per distinct queue a message has been dead-lettered from), so it stays correct even across a
// process restart mid-retry.
func (e *ConsumedEvent) RetryCount() uint32 {
	deaths, ok := e.delivery.Headers["x-death"].([]interface{})
	if !ok {
		return 0
	}
	return uint32(len(deaths))
}

// RetryOrGiveUp retries this message with backoff per policy, or gives up (final DLQ) once every
// delay in it is exhausted. Always resolves the original delivery one way or the other.
func (e *ConsumedEvent) RetryOrGiveUp(ctx context.Context, policy *RetryPolicy) error {
	attempt := int(e.RetryCount())
	if attempt >= len(policy.Delays) {
		slog.Warn("retry attempts exhausted, routed to dead-letter queue", "queue", e.queue, "attempt", attempt)
		return e.Nack(false)
	}

	retryQueue := fmt.Sprintf("%s.retry.%d", e.queue, attempt+1)
	headers := amqp.Table{}
	for k, v := range e.delivery.Headers {
		headers[k] = v
	}
	headers[originalRoutingKeyHeader] = e.RoutingKey
	err := e.retryChannel.PublishWithContext(ctx, "", retryQueue, false, false, amqp.Publishing{
		DeliveryMode: amqp.Persistent,
		Headers:      headers,
		Body:         e.Payload,
	})
	if err != nil {
		return err
	}
	return e.Ack()
}

// EventBus is the backend-agnostic publish/subscribe trait.
type EventBus interface {
	Publish(ctx context.Context, topic string, payload json.RawMessage) error
	// Subscribe declares a durable queue named queue, binds it to the shared topic exchange
	// with routingKey (AMQP wildcards */# allowed), and returns a channel of deliveries.
	// retryPolicy, when non-nil, additionally declares one retry-tier queue per Delays entry.
	Subscribe(ctx context.Context, queue, routingKey string, retryPolicy *RetryPolicy) (<-chan *ConsumedEvent, error)
	Close() error
}
