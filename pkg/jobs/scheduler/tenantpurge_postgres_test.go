//go:build integration

package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/control"
	"github.com/metap-org/metap/pkg/testkit"
)

// A tenant whose retention has run out is marked deleted by the sweep; one still inside its
// retention is left alone.
func TestRunTenantPurgeDeletesOnlyDueTenants(t *testing.T) {
	pool := testkit.NewPostgres(t)
	ctx := context.Background()
	reg := control.NewPostgresTenantRegistry(pool)

	due, kept := uuid.New(), uuid.New()
	for _, id := range []uuid.UUID{due, kept} {
		require.NoError(t, reg.RegisterShared(ctx, id, "tenant "+id.String()[:8], "trial", "active"))
	}
	now := time.Now()
	require.NoError(t, reg.RequestDeletion(ctx, due, now.Add(-48*time.Hour), time.Hour))
	require.NoError(t, reg.RequestDeletion(ctx, kept, now, 30*24*time.Hour))

	runCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	RunTenantPurge(runCtx, pool, time.Hour) // the first pass runs immediately

	status := func(id uuid.UUID) string {
		d, err := reg.GetDetail(ctx, id)
		require.NoError(t, err)
		return d.Status.String()
	}
	assert.Equal(t, "deleted", status(due))
	assert.Equal(t, "suspended", status(kept), "inside its retention: only suspended")
}
