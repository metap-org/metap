package db

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pgxpool connects lazily, so these only check the size the pool is built with.
func TestConnectPoolSize(t *testing.T) {
	ctx := context.Background()

	pool, err := Connect(ctx, "postgres://u:p@127.0.0.1:1/db")
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	assert.EqualValues(t, defaultMaxConns, pool.Config().MaxConns, "no pool_max_conns: the default")

	sized, err := Connect(ctx, "postgres://u:p@127.0.0.1:1/db?pool_max_conns=40")
	require.NoError(t, err)
	t.Cleanup(sized.Close)
	assert.EqualValues(t, 40, sized.Config().MaxConns, "pool_max_conns in the URL is honored")
}
