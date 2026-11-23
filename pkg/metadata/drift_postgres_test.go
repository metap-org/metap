//go:build integration

package metadata

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/testkit"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	return testkit.NewPostgres(t, testkit.WithSearchPath("metadata", "public"))
}

func TestMetadataDriftRecordsFirstBootThenDetectsChange(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	entityName := "test.drift." + uuid.NewString()

	CheckDrift(ctx, pool, []EntitySummary{{Name: entityName, Version: "hash-v1"}})
	var stored string
	require.NoError(t, pool.QueryRow(ctx, "SELECT hash FROM metadata_versions WHERE entity_name = $1", entityName).Scan(&stored))
	require.Equal(t, "hash-v1", stored, "expected hash-v1, got %q", stored)

	// Second call with a different hash — drift is logged, row is updated either way.
	CheckDrift(ctx, pool, []EntitySummary{{Name: entityName, Version: "hash-v2"}})
	require.NoError(t, pool.QueryRow(ctx, "SELECT hash FROM metadata_versions WHERE entity_name = $1", entityName).Scan(&stored))
	require.Equal(t, "hash-v2", stored, "expected hash-v2, got %q", stored)

	pool.Exec(ctx, "DELETE FROM metadata_versions WHERE entity_name = $1", entityName)
}
