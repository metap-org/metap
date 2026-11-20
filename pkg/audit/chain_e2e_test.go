//go:build integration

package audit

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func record(t *testing.T, s *PostgresStore, tenant uuid.UUID, entity string, actor *uuid.UUID, at time.Time, field string) {
	t.Helper()
	require.NoError(t, s.Record(context.Background(), tenant, Entry{TenantID: tenant, Entity: entity, RecordID: uuid.New(),
		Action: ActionUpdate, ActorUserID: actor, OccurredAt: at,
		Diff: JsonObject{field: json.RawMessage(`{"before":1,"after":2.50}`)}}))
}

// Roadmap 115 M9: each tenant has its own chain; editing, deleting or reordering a row is found.
func TestChainVerifiesAndDetectsTampering(t *testing.T) {
	pool := testPool(t)
	s := NewPostgresStore(pool)
	ctx := context.Background()
	a, b := uuid.New(), uuid.New()
	now := time.Now()
	for i := 0; i < 5; i++ {
		record(t, s, a, "waf.zones", nil, now.Add(time.Duration(i)*time.Second), "name")
	}
	record(t, s, b, "waf.zones", nil, now, "name")

	rep, err := s.VerifyChain(ctx, a)
	require.NoError(t, err)
	assert.True(t, rep.OK, "%+v", rep)
	assert.EqualValues(t, 5, rep.Checked)
	assert.EqualValues(t, 5, rep.HeadSeq)
	other, err := s.VerifyChain(ctx, b)
	require.NoError(t, err)
	assert.True(t, other.OK)
	assert.EqualValues(t, 1, other.HeadSeq, "chains are per tenant")

	empty, err := s.VerifyChain(ctx, uuid.New())
	require.NoError(t, err)
	assert.True(t, empty.OK, "a tenant with no history has an empty, valid chain")

	// Edit a value: that entry's hash no longer matches.
	_, err = pool.Exec(ctx, `UPDATE metadata.audit_trail_entries SET reason = 'forged' WHERE tenant_id = $1 AND seq = 3`, a)
	require.NoError(t, err)
	rep, err = s.VerifyChain(ctx, a)
	require.NoError(t, err)
	assert.False(t, rep.OK)
	assert.EqualValues(t, 3, rep.BrokenSeq)
	assert.Equal(t, "hash", rep.Reason)
	_, err = pool.Exec(ctx, `UPDATE metadata.audit_trail_entries SET reason = NULL WHERE tenant_id = $1 AND seq = 3`, a)
	require.NoError(t, err)

	// Delete a middle entry: a gap.
	_, err = pool.Exec(ctx, `DELETE FROM metadata.audit_trail_entries WHERE tenant_id = $1 AND seq = 2`, a)
	require.NoError(t, err)
	rep, err = s.VerifyChain(ctx, a)
	require.NoError(t, err)
	assert.False(t, rep.OK)
	assert.Equal(t, "gap", rep.Reason)
	assert.EqualValues(t, 2, rep.BrokenSeq)
}

func TestChainDetectsATruncatedTail(t *testing.T) {
	pool := testPool(t)
	s := NewPostgresStore(pool)
	ctx := context.Background()
	tenant := uuid.New()
	for i := 0; i < 3; i++ {
		record(t, s, tenant, "waf.zones", nil, time.Now(), "name")
	}
	_, err := pool.Exec(ctx, `DELETE FROM metadata.audit_trail_entries WHERE tenant_id = $1 AND seq = 3`, tenant)
	require.NoError(t, err)
	rep, err := s.VerifyChain(ctx, tenant)
	require.NoError(t, err)
	assert.False(t, rep.OK)
	assert.Equal(t, "head", rep.Reason)
}

// Concurrent writers of one tenant still produce one unbroken chain.
func TestChainSurvivesConcurrentWriters(t *testing.T) {
	pool := testPool(t)
	s := NewPostgresStore(pool)
	tenant := uuid.New()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			record(t, s, tenant, "waf.zones", nil, time.Now(), "name")
		}()
	}
	wg.Wait()
	rep, err := s.VerifyChain(context.Background(), tenant)
	require.NoError(t, err)
	assert.True(t, rep.OK, "%+v", rep)
	assert.EqualValues(t, 20, rep.Checked)
}

func TestSearchFiltersByPersonObjectAndTime(t *testing.T) {
	s := NewPostgresStore(testPool(t))
	ctx := context.Background()
	tenant := uuid.New()
	alice, bob := uuid.New(), uuid.New()
	base := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	record(t, s, tenant, "waf.zones", &alice, base, "name")
	record(t, s, tenant, "waf.rules", &alice, base.Add(10*time.Minute), "expr")
	record(t, s, tenant, "waf.zones", &bob, base.Add(20*time.Minute), "name")
	record(t, s, uuid.New(), "waf.zones", &alice, base, "name") // another tenant

	all, err := s.Search(ctx, tenant, SearchQuery{})
	require.NoError(t, err)
	assert.Len(t, all, 3, "tenant only")
	assert.Equal(t, bob, *all[0].ActorUserID, "newest first")
	require.NotNil(t, all[0].Seq)
	assert.EqualValues(t, 3, *all[0].Seq)
	assert.NotEmpty(t, all[0].Hash)

	byAlice, err := s.Search(ctx, tenant, SearchQuery{ActorUserID: &alice})
	require.NoError(t, err)
	assert.Len(t, byAlice, 2)
	zones, err := s.Search(ctx, tenant, SearchQuery{Entity: "waf.zones"})
	require.NoError(t, err)
	assert.Len(t, zones, 2)
	from, to := base.Add(5*time.Minute), base.Add(15*time.Minute)
	window, err := s.Search(ctx, tenant, SearchQuery{From: &from, To: &to})
	require.NoError(t, err)
	require.Len(t, window, 1)
	assert.Equal(t, "waf.rules", window[0].Entity)

	page, err := s.Search(ctx, tenant, SearchQuery{Limit: 2})
	require.NoError(t, err)
	assert.Len(t, page, 3, "limit+1 rows signal another page")
}
