package eventbus

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"

	amqp "github.com/rabbitmq/amqp091-go"
)

// RabbitEventBus is the only EventBus impl today.
type RabbitEventBus struct {
	conn    *amqp.Connection
	channel *amqp.Channel
}

// ConnectRabbitEventBus dials url and declares the shared topic exchange.
func ConnectRabbitEventBus(url string) (*RabbitEventBus, error) {
	conn, err := amqp.Dial(url)
	if err != nil {
		return nil, err
	}
	ch, err := conn.Channel()
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if err := ch.ExchangeDeclare(exchange, amqp.ExchangeTopic, true, false, false, false, nil); err != nil {
		_ = conn.Close()
		return nil, err
	}
	slog.Info("connected to rabbitmq", "exchange", exchange)
	return &RabbitEventBus{conn: conn, channel: ch}, nil
}

// RabbitMQConnector returns a reconnect closure — every long-running consumer in this module
// takes one so a RabbitMQ blip mid-run can reconnect with backoff instead of dying.
func RabbitMQConnector(url string) func() (EventBus, error) {
	return func() (EventBus, error) {
		return ConnectRabbitEventBus(url)
	}
}

func (b *RabbitEventBus) Publish(ctx context.Context, topic string, payload json.RawMessage) error {
	err := b.channel.PublishWithContext(ctx, exchange, topic, false, false, amqp.Publishing{
		ContentType:  "application/json",
		DeliveryMode: amqp.Persistent,
		Body:         payload,
	})
	if err != nil {
		return err
	}
	slog.Debug("published event", "topic", topic)
	return nil
}

// prefetchCount bounds how many unacked deliveries the broker will push ahead of the consumer.
const prefetchCount = 20

func (b *RabbitEventBus) Subscribe(ctx context.Context, queue, routingKey string, retryPolicy *RetryPolicy) (<-chan *ConsumedEvent, error) {
	// A dedicated channel per subscription — consuming holds a channel open for the stream's
	// whole lifetime, and sharing b.channel with Publish would let a slow consumer stall
	// publishes on the same connection.
	ch, err := b.conn.Channel()
	if err != nil {
		return nil, err
	}

	dlqName := queue + ".dlq"
	if _, err := ch.QueueDeclare(dlqName, true, false, false, false, nil); err != nil {
		return nil, err
	}

	queueArgs := amqp.Table{
		"x-dead-letter-exchange":    "",
		"x-dead-letter-routing-key": dlqName,
	}
	if _, err := ch.QueueDeclare(queue, true, false, false, false, queueArgs); err != nil {
		return nil, err
	}
	if err := ch.QueueBind(queue, routingKey, exchange, false, nil); err != nil {
		return nil, err
	}

	// Retry tiers: <queue>.retry.1, <queue>.retry.2, ... — a message sitting here until its
	// x-message-ttl expires, at which point RabbitMQ dead-letters it back onto queue (the
	// classic TTL+DLX "delay queue" pattern).
	if retryPolicy != nil {
		for i, delay := range retryPolicy.Delays {
			tierQueue := queue + ".retry." + strconv.Itoa(i+1)
			tierArgs := amqp.Table{
				"x-message-ttl":             delay.Milliseconds(),
				"x-dead-letter-exchange":    "",
				"x-dead-letter-routing-key": queue,
			}
			if _, err := ch.QueueDeclare(tierQueue, true, false, false, false, tierArgs); err != nil {
				return nil, err
			}
		}
	}

	if err := ch.Qos(prefetchCount, 0, false); err != nil {
		return nil, err
	}

	deliveries, err := ch.Consume(queue, queue, false, false, false, false, nil)
	if err != nil {
		return nil, err
	}
	slog.Info("subscribed", "queue", queue, "routing_key", routingKey)

	out := make(chan *ConsumedEvent)
	go func() {
		defer close(out)
		for delivery := range deliveries {
			var payload json.RawMessage
			if err := json.Unmarshal(delivery.Body, &payload); err != nil {
				slog.Warn("dropping malformed message, routed to dead-letter queue", "queue", queue, "error", err)
				_ = delivery.Nack(false, false)
				continue
			}
			rk := delivery.RoutingKey
			if orig, ok := delivery.Headers[originalRoutingKeyHeader].(string); ok {
				rk = orig
			}
			select {
			case out <- &ConsumedEvent{RoutingKey: rk, Payload: payload, delivery: delivery, retryChannel: ch, queue: queue}:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

func (b *RabbitEventBus) Close() error {
	_ = b.channel.Close()
	return b.conn.Close()
}
