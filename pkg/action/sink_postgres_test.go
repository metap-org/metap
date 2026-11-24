//go:build integration

package action

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/messaging/events"
	"github.com/metap-org/metap/pkg/secrets"
	"github.com/metap-org/metap/pkg/tenancy"
	"github.com/metap-org/metap/pkg/testkit"
)

func testSink(t *testing.T) (Sink, *pgxpool.Pool) {
	t.Helper()
	pool := testkit.NewPostgres(t, testkit.WithSearchPath("metadata", "public"))
	router := tenancy.NewRouter(pool, tenancy.NewRegistryCache(tenancy.NewPostgresTenantRegistry(pool)), secrets.EnvStore{})
	return NewSink(router, nil), pool
}

func countOutbox(t *testing.T, pool *pgxpool.Pool, aggregate uuid.UUID) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(context.Background(), "SELECT count(*) FROM outbox_events WHERE aggregate_id = $1", aggregate).Scan(&n))
	return n
}

func TestTheSinkEnqueuesEveryEventInOneTransaction(t *testing.T) {
	sink, pool := testSink(t)
	aggregate := uuid.New()
	evs := []events.Event{
		{Topic: "waf.zone.verified", AggregateType: "waf.zones", AggregateID: aggregate, Payload: map[string]any{"ok": true}},
		{Topic: "waf.zone.activated", AggregateType: "waf.zones", AggregateID: aggregate},
	}
	err := sink.Atomic(context.Background(), uuid.New(), func(_ context.Context, enqueue func([]events.Event) error) error {
		return enqueue(evs)
	})
	require.NoError(t, err)
	require.Equal(t, 2, countOutbox(t, pool, aggregate))
}

func TestOneBadEventInABatchLeavesNoneOfThemInTheOutbox(t *testing.T) {
	sink, pool := testSink(t)
	aggregate := uuid.New()
	err := sink.Atomic(context.Background(), uuid.New(), func(_ context.Context, enqueue func([]events.Event) error) error {
		return enqueue([]events.Event{
			{Topic: "waf.zone.verified", AggregateType: "waf.zones", AggregateID: aggregate},
			{Topic: "NOT A TOPIC", AggregateType: "waf.zones", AggregateID: aggregate},
		})
	})
	require.Error(t, err)
	require.Equal(t, 0, countOutbox(t, pool, aggregate), "the first event rolled back with the second")
}

func TestAnErrorFromTheWorkRollsBackTheEventsAlreadyEnqueued(t *testing.T) {
	sink, pool := testSink(t)
	aggregate := uuid.New()
	boom := errors.New("the work failed after enqueuing")
	err := sink.Atomic(context.Background(), uuid.New(), func(_ context.Context, enqueue func([]events.Event) error) error {
		if err := enqueue([]events.Event{{Topic: "waf.zone.verified", AggregateType: "waf.zones", AggregateID: aggregate}}); err != nil {
			return err
		}
		return boom
	})
	require.ErrorIs(t, err, boom)
	require.Equal(t, 0, countOutbox(t, pool, aggregate))
}
