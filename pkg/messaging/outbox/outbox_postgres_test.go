//go:build integration

package outbox

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/testkit"
)

func TestEnqueueWritesARealRow(t *testing.T) {
	ctx := context.Background()
	// outbox_events is a "metadata"-schema table referenced bare — search_path is set on every
	// pooled connection the same way Router.Begin does in the real deployment.
	pool := testkit.NewPostgres(t, testkit.WithSearchPath("metadata", "public"))

	aggregateID := uuid.New()
	err := Enqueue(ctx, pool, &Event{
		Topic: "test.go.port.wave2", AggregateType: "test.entity", AggregateID: aggregateID,
		Payload: []byte(`{"hello":"world"}`),
	})
	require.NoError(t, err)

	var count int
	require.NoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM outbox_events WHERE aggregate_id = $1", aggregateID).Scan(&count))
	require.Equal(t, 1, count, "got %d rows, want 1", count)
	_, _ = pool.Exec(ctx, "DELETE FROM outbox_events WHERE aggregate_id = $1", aggregateID)
}
