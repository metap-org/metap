//go:build integration

package audit

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func pendingCount(t *testing.T, s *PostgresStore) int {
	t.Helper()
	var n int
	require.NoError(t, s.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM metadata.outbox_events WHERE topic = $1 AND published_at IS NULL`, Topic).Scan(&n))
	return n
}

func sampleEntry(tenant uuid.UUID, action AuditAction) Entry {
	name := "zone.verified"
	return Entry{TenantID: tenant, Entity: "waf.zones", RecordID: uuid.New(), Action: action, TransitionAction: &name,
		Diff: JsonObject{"detail": json.RawMessage(`{"ok":true}`)}, OccurredAt: time.Now().UTC()}
}

// An entry written to the outbox is not in the chain until the drain moves it, and then lands
// once, in the tenant's chain, which still verifies.
func TestAnEnqueuedEntryReachesTheChainThroughTheDrain(t *testing.T) {
	pool := testPool(t)
	s := NewPostgresStore(pool)
	ctx := context.Background()
	tenant := uuid.New()
	e := sampleEntry(tenant, ActionCustom)
	require.NoError(t, Enqueue(ctx, pool, e))
	require.Equal(t, 1, pendingCount(t, s))

	rows, err := s.Search(ctx, tenant, SearchQuery{})
	require.NoError(t, err)
	require.Empty(t, rows, "not in the chain before the drain")

	require.NoError(t, DrainAll(ctx, pool, s))
	require.Equal(t, 0, pendingCount(t, s))
	rows, err = s.Search(ctx, tenant, SearchQuery{})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "action", rows[0].Action)
	require.Equal(t, "zone.verified", *rows[0].TransitionAction)

	report, err := s.VerifyChain(ctx, tenant)
	require.NoError(t, err)
	require.True(t, report.OK, "%+v", report)
}

// A row delivered twice (the drain died between writing the entry and marking the row) lands once.
func TestDrainingTheSameRowTwiceWritesItOnce(t *testing.T) {
	pool := testPool(t)
	s := NewPostgresStore(pool)
	ctx := context.Background()
	tenant := uuid.New()
	require.NoError(t, Enqueue(ctx, pool, sampleEntry(tenant, ActionCustom)))

	var id uuid.UUID
	var payload []byte
	require.NoError(t, pool.QueryRow(ctx, `SELECT id, payload FROM metadata.outbox_events WHERE topic = $1 AND published_at IS NULL`, Topic).Scan(&id, &payload))
	entry, err := decodePending(payload)
	require.NoError(t, err)
	require.NoError(t, s.RecordOnce(ctx, tenant, id, entry))
	// the crash: the entry is in the chain, the row is still pending
	require.Equal(t, 1, pendingCount(t, s))
	require.NoError(t, DrainAll(ctx, pool, s))

	rows, err := s.Search(ctx, tenant, SearchQuery{})
	require.NoError(t, err)
	require.Len(t, rows, 1, "the redelivered row did not write a second entry")
	require.Equal(t, id, rows[0].ID, "the entry carries the outbox row's id")
	report, err := s.VerifyChain(ctx, tenant)
	require.NoError(t, err)
	require.True(t, report.OK)
}

// Entries of one tenant keep the order they were enqueued in, one chain per tenant.
func TestTheDrainKeepsEachTenantsEntriesInOrder(t *testing.T) {
	pool := testPool(t)
	s := NewPostgresStore(pool)
	ctx := context.Background()
	a, b := uuid.New(), uuid.New()
	for i := 0; i < 5; i++ {
		require.NoError(t, Enqueue(ctx, pool, sampleEntry(a, ActionCustom)))
		require.NoError(t, Enqueue(ctx, pool, sampleEntry(b, ActionCustom)))
	}
	require.NoError(t, DrainAll(ctx, pool, s))
	for _, tenant := range []uuid.UUID{a, b} {
		report, err := s.VerifyChain(ctx, tenant)
		require.NoError(t, err)
		require.True(t, report.OK)
		require.EqualValues(t, 5, report.Checked)
	}
}

// A row that cannot be decoded is set aside with its error, not retried forever and not blocking
// the entries behind it.
func TestAnUndecodableRowIsSetAsideAndDoesNotBlockTheOthers(t *testing.T) {
	pool := testPool(t)
	s := NewPostgresStore(pool)
	ctx := context.Background()
	tenant := uuid.New()
	_, err := pool.Exec(ctx, `INSERT INTO metadata.outbox_events (topic, aggregate_type, aggregate_id, payload) VALUES ($1, 'x', $2, '{"v":1,"entry":{"action":"nonsense"}}')`, Topic, uuid.New())
	require.NoError(t, err)
	require.NoError(t, Enqueue(ctx, pool, sampleEntry(tenant, ActionCustom)))

	require.NoError(t, DrainAll(ctx, pool, s))
	require.Equal(t, 0, pendingCount(t, s))
	var failed int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM metadata.outbox_events WHERE topic = $1 AND last_error LIKE 'undecodable%'`, Topic).Scan(&failed))
	require.Equal(t, 1, failed)
	rows, err := s.Search(ctx, tenant, SearchQuery{})
	require.NoError(t, err)
	require.Len(t, rows, 1)
}
