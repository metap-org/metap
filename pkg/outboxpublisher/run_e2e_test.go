//go:build integration

package outboxpublisher

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/metap-org/metap/pkg/testkit"
	"github.com/stretchr/testify/require"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/metap-org/metap/pkg/infra/eventbus"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := testkit.NewPostgres(t, testkit.WithSearchPath("metadata", "public"))
	return pool
}

// fakeBus is an in-memory EventBus — this test only exercises the Postgres side of
// publishPending, not a real RabbitMQ round trip, so a fake avoids needing one running.
type fakeBus struct {
	published []string // topics
	failTopic string   // if set, Publish fails for exactly this topic
}

func (b *fakeBus) Publish(ctx context.Context, topic string, payload json.RawMessage) error {
	if topic == b.failTopic {
		return errors.New("simulated publish failure")
	}
	b.published = append(b.published, topic)
	return nil
}
func (b *fakeBus) Subscribe(ctx context.Context, queue, routingKey string, retryPolicy *eventbus.RetryPolicy) (<-chan *eventbus.ConsumedEvent, error) {
	return nil, errors.New("not implemented in fakeBus")
}
func (b *fakeBus) Close() error { return nil }

func insertOutboxRow(t *testing.T, pool *pgxpool.Pool, topic string) uuid.UUID {
	id := uuid.New()
	_, err := pool.Exec(context.Background(),
		"INSERT INTO outbox_events (id, topic, aggregate_type, aggregate_id, payload) VALUES ($1, $2, $3, $4, $5)",
		id, topic, "test.entity", uuid.New(), []byte(`{"hello":"world"}`))
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), "DELETE FROM outbox_events WHERE id = $1", id) })
	return id
}

func TestPublishPendingPublishesAnUnpublishedRowAndMarksItDone(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	topic := "test.go.port.outbox." + uuid.NewString()
	id := insertOutboxRow(t, pool, topic)

	bus := &fakeBus{}
	allPublished, err := publishPending(ctx, pool, bus, 100)
	require.NoError(t, err)
	require.True(t, allPublished, "expected all rows published")
	found := false
	for _, p := range bus.published {
		if p == topic {
			found = true
		}
	}
	require.True(t, found, "expected %q to have been published, got %v", topic, bus.published)

	var publishedAt *string
	require.NoError(t, pool.QueryRow(ctx, "SELECT published_at::text FROM outbox_events WHERE id = $1", id).Scan(&publishedAt))
	require.NotNil(t, publishedAt, "expected published_at to be set")
}

func TestPublishPendingLeavesAFailedRowUnpublishedAndRecordsTheError(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	topic := "test.go.port.outbox.fail." + uuid.NewString()
	id := insertOutboxRow(t, pool, topic)

	bus := &fakeBus{failTopic: topic}
	allPublished, err := publishPending(ctx, pool, bus, 100)
	require.NoError(t, err)
	require.False(t, allPublished, "expected allPublished = false")

	var publishedAt *string
	var attempts int
	var lastError *string
	require.NoError(t, pool.QueryRow(ctx, "SELECT published_at::text, attempts, last_error FROM outbox_events WHERE id = $1", id).
		Scan(&publishedAt, &attempts, &lastError))
	require.Nil(t, publishedAt, "expected published_at to stay NULL after a publish failure")
	require.Equal(t, 1, attempts, "expected attempts = 1, got %d", attempts)
	require.NotNil(t, lastError, "expected last_error to be recorded")
	require.NotEqual(t, "", *lastError, "expected last_error to be recorded")
}

func TestPublishPendingDoesNotRepublishAnAlreadyPublishedRow(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	topic := "test.go.port.outbox.already." + uuid.NewString()
	id := insertOutboxRow(t, pool, topic)
	_, err := pool.Exec(ctx, "UPDATE outbox_events SET published_at = now() WHERE id = $1", id)
	require.NoError(t, err)

	bus := &fakeBus{}
	_, err = publishPending(ctx, pool, bus, 100)
	require.NoError(t, err)
	for _, p := range bus.published {
		require.NotEqual(t, topic, p, "an already-published row must not be republished")
	}
}
