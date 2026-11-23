//go:build integration

package db

import (
	"context"
	"testing"

	"github.com/metap-org/metap/pkg/testkit"
	"github.com/stretchr/testify/require"
)

func TestConnectAndHealthCheckAgainstRealPostgres(t *testing.T) {
	ctx := context.Background()
	pool, err := Connect(ctx, testkit.NewDatabaseURL(t))
	require.NoError(t, err)
	defer pool.Close()
	require.True(t, HealthCheck(ctx, pool), "expected health check to pass against a real Postgres")
}
