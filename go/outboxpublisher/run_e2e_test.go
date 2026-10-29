package outboxpublisher

// The Rust metap-outbox-publisher crate has no unit tests of its own (confirmed by reading its
// source) — this is new test coverage added during the port, verified against a real Postgres
// rather than mocked, matching this project's own "verify sống" convention.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/metap-org/metap-go/infra/eventbus"
)

func testPool(t *testing.T) *pgxpool.Pool {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		url = "postgres://metap:metap@localhost:5432/metap"
	}
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, "SET search_path TO metadata, public")
		return err
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Skipf("no Postgres available (%v) — skipping e2e test", err)
	}
	if err := pool.Ping(context.Background()); err != nil {
		t.Skipf("no Postgres available (%v) — skipping e2e test", err)
	}
	t.Cleanup(pool.Close)
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
	if err != nil {
		t.Fatal(err)
	}
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
	if err != nil {
		t.Fatal(err)
	}
	if !allPublished {
		t.Fatal("expected all rows published")
	}
	found := false
	for _, p := range bus.published {
		if p == topic {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected %q to have been published, got %v", topic, bus.published)
	}

	var publishedAt *string
	if err := pool.QueryRow(ctx, "SELECT published_at::text FROM outbox_events WHERE id = $1", id).Scan(&publishedAt); err != nil {
		t.Fatal(err)
	}
	if publishedAt == nil {
		t.Fatal("expected published_at to be set")
	}
}

func TestPublishPendingLeavesAFailedRowUnpublishedAndRecordsTheError(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	topic := "test.go.port.outbox.fail." + uuid.NewString()
	id := insertOutboxRow(t, pool, topic)

	bus := &fakeBus{failTopic: topic}
	allPublished, err := publishPending(ctx, pool, bus, 100)
	if err != nil {
		t.Fatal(err)
	}
	if allPublished {
		t.Fatal("expected allPublished = false")
	}

	var publishedAt *string
	var attempts int
	var lastError *string
	if err := pool.QueryRow(ctx, "SELECT published_at::text, attempts, last_error FROM outbox_events WHERE id = $1", id).
		Scan(&publishedAt, &attempts, &lastError); err != nil {
		t.Fatal(err)
	}
	if publishedAt != nil {
		t.Fatal("expected published_at to stay NULL after a publish failure")
	}
	if attempts != 1 {
		t.Fatalf("expected attempts = 1, got %d", attempts)
	}
	if lastError == nil || *lastError == "" {
		t.Fatal("expected last_error to be recorded")
	}
}

func TestPublishPendingDoesNotRepublishAnAlreadyPublishedRow(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	topic := "test.go.port.outbox.already." + uuid.NewString()
	id := insertOutboxRow(t, pool, topic)
	if _, err := pool.Exec(ctx, "UPDATE outbox_events SET published_at = now() WHERE id = $1", id); err != nil {
		t.Fatal(err)
	}

	bus := &fakeBus{}
	if _, err := publishPending(ctx, pool, bus, 100); err != nil {
		t.Fatal(err)
	}
	for _, p := range bus.published {
		if p == topic {
			t.Fatal("an already-published row must not be republished")
		}
	}
}
