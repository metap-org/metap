//go:build integration

package httpapi_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/auth/identity"
	"github.com/metap-org/metap/pkg/tenancy"
)

// The emailed link reopens a workspace whose deletion was requested, once, with no session.
func TestCancelWorkspaceDeletionByLink(t *testing.T) {
	pool := httpTestPool(t)
	ctx := context.Background()
	tenant := uuid.New()
	registry := tenancy.NewPostgresTenantRegistry(pool)
	require.NoError(t, registry.RegisterShared(ctx, tenant, "Acme", "trial", "active"))
	now := time.Now()
	require.NoError(t, registry.RequestDeletion(ctx, tenant, now, tenancy.DefaultDeletionRetention))
	token, err := identity.CreateDeletionCancelToken(ctx, pool, tenant, now.Add(tenancy.DefaultDeletionRetention))
	require.NoError(t, err)

	state, _ := newTestAppState(t, pool, httpTestEntity())
	base := startTestServer(t, state, nil)

	res := doRequest(t, "POST", base+"/auth/workspace-deletion/cancel", "", map[string]any{"token": "nope"})
	assert.Equal(t, 400, res.StatusCode)
	res = doRequest(t, "POST", base+"/auth/workspace-deletion/cancel", "", map[string]any{"token": token})
	require.Equal(t, 200, res.StatusCode)
	d, err := registry.Get(ctx, tenant)
	require.NoError(t, err)
	assert.Equal(t, tenancy.StatusActive, d.Status)
	res = doRequest(t, "POST", base+"/auth/workspace-deletion/cancel", "", map[string]any{"token": token})
	assert.Equal(t, 400, res.StatusCode, "a link works once")
}
