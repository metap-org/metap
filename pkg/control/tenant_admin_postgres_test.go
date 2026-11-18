//go:build integration

package control

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/testkit"
)

func TestTenantLifecycle(t *testing.T) {
	ctx := context.Background()
	pool := testkit.NewPostgres(t)
	reg := NewPostgresTenantRegistry(pool)
	id := uuid.New()
	require.NoError(t, reg.RegisterShared(ctx, id, "Acme Corp", "trial", "active"))

	d, err := reg.GetDetail(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, "Acme Corp", d.Name)
	assert.Equal(t, StatusActive, d.Status)
	assert.Equal(t, StrategySchema, d.Strategy.Kind)

	_, err = reg.GetDetail(ctx, uuid.New())
	assert.ErrorIs(t, err, ErrTenantNotFound)

	name := "Acme Inc"
	require.NoError(t, reg.Update(ctx, id, &name, nil))
	d, _ = reg.GetDetail(ctx, id)
	assert.Equal(t, "Acme Inc", d.Name)
	assert.Equal(t, "trial", d.Tier, "a nil field is left alone")
	assert.ErrorIs(t, reg.Update(ctx, uuid.New(), &name, nil), ErrTenantNotFound)

	// suspend / resume
	require.NoError(t, reg.Suspend(ctx, id))
	assert.ErrorIs(t, reg.Suspend(ctx, id), ErrTenantState, "already suspended")
	require.NoError(t, reg.Resume(ctx, id))
	assert.ErrorIs(t, reg.Resume(ctx, id), ErrTenantState, "already active")

	// deletion with retention
	now := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, reg.RequestDeletion(ctx, id, now, 10*24*time.Hour))
	d, _ = reg.GetDetail(ctx, id)
	assert.Equal(t, StatusSuspended, d.Status, "suspended at once")
	require.NotNil(t, d.DeletionEffectiveAt)
	assert.WithinDuration(t, now.Add(10*24*time.Hour), *d.DeletionEffectiveAt, time.Second)
	assert.ErrorIs(t, reg.RequestDeletion(ctx, id, now, 0), ErrTenantState, "no second request")
	assert.ErrorIs(t, reg.Resume(ctx, id), ErrTenantState, "Resume must not void a deletion request")

	purged, err := reg.PurgeDue(ctx, now.Add(9*24*time.Hour))
	require.NoError(t, err)
	assert.Empty(t, purged, "retention not over yet")

	require.NoError(t, reg.CancelDeletion(ctx, id, now.Add(time.Hour)))
	d, _ = reg.GetDetail(ctx, id)
	assert.Equal(t, StatusActive, d.Status)
	assert.Nil(t, d.DeletionRequestedAt)
	assert.ErrorIs(t, reg.CancelDeletion(ctx, id, now), ErrTenantState, "nothing to cancel")

	// a request that runs out is purged; after that nothing can reopen the tenant
	require.NoError(t, reg.RequestDeletion(ctx, id, now, time.Hour))
	purged, err = reg.PurgeDue(ctx, now.Add(2*time.Hour))
	require.NoError(t, err)
	assert.Equal(t, []uuid.UUID{id}, purged)
	d, _ = reg.GetDetail(ctx, id)
	assert.Equal(t, StatusDeleted, d.Status)
	assert.ErrorIs(t, reg.CancelDeletion(ctx, id, now), ErrTenantState)
	assert.ErrorIs(t, reg.Resume(ctx, id), ErrTenantState)
	assert.ErrorIs(t, reg.Suspend(ctx, id), ErrTenantState)
	purged, _ = reg.PurgeDue(ctx, now.Add(3*time.Hour))
	assert.Empty(t, purged, "purging is idempotent")
}

func TestTenantSearch(t *testing.T) {
	ctx := context.Background()
	pool := testkit.NewPostgres(t)
	reg := NewPostgresTenantRegistry(pool)
	ids := map[string]uuid.UUID{}
	for _, n := range []string{"Acme Corp", "Beta 100%", "Gamma"} {
		ids[n] = uuid.New()
		require.NoError(t, reg.RegisterShared(ctx, ids[n], n, "trial", "active"))
	}
	require.NoError(t, reg.Suspend(ctx, ids["Gamma"]))

	all, total, err := reg.Search(ctx, TenantFilter{})
	require.NoError(t, err)
	assert.Equal(t, 3, total)
	assert.Len(t, all, 3)

	got, total, _ := reg.Search(ctx, TenantFilter{Query: "acme"})
	assert.Equal(t, 1, total)
	assert.Equal(t, "Acme Corp", got[0].Name, "case-insensitive substring")

	got, _, _ = reg.Search(ctx, TenantFilter{Query: "100%"})
	require.Len(t, got, 1)
	assert.Equal(t, "Beta 100%", got[0].Name, "% is literal, not a wildcard")
	got, _, _ = reg.Search(ctx, TenantFilter{Query: "%"})
	assert.Len(t, got, 1, "a bare % matches only names containing one")

	got, _, _ = reg.Search(ctx, TenantFilter{Query: ids["Gamma"].String()})
	require.Len(t, got, 1, "exact id")
	assert.Equal(t, "Gamma", got[0].Name)

	got, total, _ = reg.Search(ctx, TenantFilter{Status: "suspended"})
	assert.Equal(t, 1, total)
	assert.Equal(t, "Gamma", got[0].Name)

	page, total, _ := reg.Search(ctx, TenantFilter{Limit: 2, Offset: 2})
	assert.Equal(t, 3, total)
	assert.Len(t, page, 1)
}
