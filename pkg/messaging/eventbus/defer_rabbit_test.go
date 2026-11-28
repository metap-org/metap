//go:build integration

package eventbus

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Defer brings a message back through the same tier as often as it is asked, past the point where
// RetryOrGiveUp would have dead-lettered it, with the routing key it was published under.
func TestDeferRedeliversAsOftenAsAsked(t *testing.T) {
	url := os.Getenv("RABBITMQ_URL")
	if url == "" {
		url = "amqp://metap:metap@localhost:5672"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	bus, err := ConnectRabbitEventBus(url)
	require.NoError(t, err, "RabbitMQ is required")
	defer bus.Close()

	queue := "test.defer." + uuid.NewString()
	topic := queue + ".due"
	policy := &RetryPolicy{Delays: []time.Duration{200 * time.Millisecond}}
	events, err := bus.Subscribe(ctx, queue, topic, policy)
	require.NoError(t, err)
	t.Cleanup(func() {
		conn, err := amqp.Dial(url)
		if err != nil {
			return
		}
		defer conn.Close()
		ch, err := conn.Channel()
		if err != nil {
			return
		}
		for _, name := range []string{queue, queue + ".retry.1", queue + ".dlq"} {
			_, _ = ch.QueueDelete(name, false, false, false)
		}
	})
	require.NoError(t, bus.Publish(ctx, topic, json.RawMessage(`{"n":1}`)))

	next := func() *ConsumedEvent {
		select {
		case e := <-events:
			return e
		case <-ctx.Done():
			require.FailNow(t, "the message did not come back")
			return nil
		}
	}
	for range 4 {
		e := next()
		assert.Equal(t, topic, e.RoutingKey)
		assert.JSONEq(t, `{"n":1}`, string(e.Payload))
		require.NoError(t, e.Defer(ctx, policy))
	}
	require.NoError(t, next().Ack())
	assert.Error(t, (&ConsumedEvent{}).Defer(ctx, nil))
}
