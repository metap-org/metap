package infra

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func testDatabaseURL() string {
	if v := os.Getenv("DATABASE_URL"); v != "" {
		return v
	}
	return "postgres://metap:metap@localhost:5432/metap"
}

func TestConnectAndHealthCheckAgainstRealPostgres(t *testing.T) {
	ctx := context.Background()
	pool, err := Connect(ctx, testDatabaseURL())
	if err != nil {
		t.Skipf("no Postgres available (%v) — skipping e2e test", err)
	}
	defer pool.Close()
	if !HealthCheck(ctx, pool) {
		t.Fatal("expected health check to pass against a real Postgres")
	}
}

func TestEnqueueOutboxEventWritesARealRow(t *testing.T) {
	ctx := context.Background()
	// outbox_events is a "metadata"-schema table referenced bare — AfterConnect sets
	// search_path on every pooled connection the same way Router::begin does in the real
	// deployment (a one-off Exec("SET search_path...") would only affect whichever single
	// connection happened to serve that call, not the pool as a whole).
	cfg, err := pgxpool.ParseConfig(testDatabaseURL())
	if err != nil {
		t.Fatal(err)
	}
	cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, "SET search_path TO metadata, public")
		return err
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Skipf("no Postgres available (%v) — skipping e2e test", err)
	}
	if err := pool.Ping(ctx); err != nil {
		t.Skipf("no Postgres available (%v) — skipping e2e test", err)
	}
	defer pool.Close()

	aggregateID := uuid.New()
	err = EnqueueOutboxEvent(ctx, pool, &OutboxEvent{
		Topic: "test.go.port.wave2", AggregateType: "test.entity", AggregateID: aggregateID,
		Payload: []byte(`{"hello":"world"}`),
	})
	if err != nil {
		t.Fatal(err)
	}

	var count int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM outbox_events WHERE aggregate_id = $1", aggregateID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("got %d rows, want 1", count)
	}
	_, _ = pool.Exec(ctx, "DELETE FROM outbox_events WHERE aggregate_id = $1", aggregateID)
}
