//go:build integration

package infra

import (
	"context"
	"testing"

	"github.com/metap-org/metap/pkg/testkit"
	"github.com/stretchr/testify/require"

	"github.com/google/uuid"
)

func TestConnectAndHealthCheckAgainstRealPostgres(t *testing.T) {
	ctx := context.Background()
	pool, err := Connect(ctx, testkit.NewDatabaseURL(t))
	require.NoError(t, err)
	defer pool.Close()
	require.True(t, HealthCheck(ctx, pool), "expected health check to pass against a real Postgres")
}

func TestEnqueueOutboxEventWritesARealRow(t *testing.T) {
	ctx := context.Background()
	// outbox_events is a "metadata"-schema table referenced bare — search_path is set on every
	// pooled connection the same way Router::begin does in the real deployment.
	pool := testkit.NewPostgres(t, testkit.WithSearchPath("metadata", "public"))

	aggregateID := uuid.New()
	err := EnqueueOutboxEvent(ctx, pool, &OutboxEvent{
		Topic: "test.go.port.wave2", AggregateType: "test.entity", AggregateID: aggregateID,
		Payload: []byte(`{"hello":"world"}`),
	})
	require.NoError(t, err)

	var count int
	require.NoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM outbox_events WHERE aggregate_id = $1", aggregateID).Scan(&count))
	require.Equal(t, 1, count, "got %d rows, want 1", count)
	_, _ = pool.Exec(ctx, "DELETE FROM outbox_events WHERE aggregate_id = $1", aggregateID)
}
