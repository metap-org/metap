//go:build integration

package action

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/messaging/events"
	"github.com/metap-org/metap/pkg/testkit"
)

// poolTx opens a transaction on the test pool, whose search_path includes the metadata schema the
// way Router.Begin sets it for a tenant.
type poolTx struct{ pool *pgxpool.Pool }

func (p poolTx) Begin(ctx context.Context, _ uuid.UUID) (pgx.Tx, error) { return p.pool.Begin(ctx) }

func countOutbox(t *testing.T, pool *pgxpool.Pool, aggregate uuid.UUID) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(context.Background(), "SELECT count(*) FROM outbox_events WHERE aggregate_id = $1", aggregate).Scan(&n))
	return n
}

func TestTheSinkEnqueuesEveryEventInOneTransaction(t *testing.T) {
	pool := testkit.NewPostgres(t, testkit.WithSearchPath("metadata", "public"))
	sink := NewSink(poolTx{pool}, nil)
	aggregate := uuid.New()

	err := sink.Emit(context.Background(), uuid.New(), []events.Event{
		{Topic: "waf.zone.verified", AggregateType: "waf.zones", AggregateID: aggregate, Payload: map[string]any{"ok": true}},
		{Topic: "waf.zone.activated", AggregateType: "waf.zones", AggregateID: aggregate},
	})
	require.NoError(t, err)
	require.Equal(t, 2, countOutbox(t, pool, aggregate))
}

func TestOneBadEventInABatchLeavesNoneOfThemInTheOutbox(t *testing.T) {
	pool := testkit.NewPostgres(t, testkit.WithSearchPath("metadata", "public"))
	sink := NewSink(poolTx{pool}, nil)
	aggregate := uuid.New()

	err := sink.Emit(context.Background(), uuid.New(), []events.Event{
		{Topic: "waf.zone.verified", AggregateType: "waf.zones", AggregateID: aggregate},
		{Topic: "NOT A TOPIC", AggregateType: "waf.zones", AggregateID: aggregate},
	})
	require.Error(t, err)
	require.Equal(t, 0, countOutbox(t, pool, aggregate), "the first event rolled back with the second")
}
