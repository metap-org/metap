//go:build integration

package testkit_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/testkit"
)

func TestNewPostgresGivesEachCallerAnIsolatedMigratedDatabase(t *testing.T) {
	ctx := context.Background()
	a := testkit.NewPostgres(t)
	b := testkit.NewPostgres(t)

	var nameA, nameB string
	require.NoError(t, a.QueryRow(ctx, `SELECT current_database()`).Scan(&nameA))
	require.NoError(t, b.QueryRow(ctx, `SELECT current_database()`).Scan(&nameB))
	require.NotEqual(t, nameA, nameB)

	// Migrated: the role table every permission check reads exists, and starts empty.
	tenant, user := uuid.New(), uuid.New()
	testkit.SeedRole(t, a, tenant, user, "admin")

	var inA, inB int
	require.NoError(t, a.QueryRow(ctx, `SELECT count(*) FROM metadata.user_roles`).Scan(&inA))
	require.NoError(t, b.QueryRow(ctx, `SELECT count(*) FROM metadata.user_roles`).Scan(&inB))
	require.Equal(t, 1, inA)
	require.Equal(t, 0, inB)
}

func TestNewEmptyDatabaseURLHasNoMigrationsApplied(t *testing.T) {
	ctx := context.Background()
	url := testkit.NewEmptyDatabaseURL(t)
	conn, err := pgx.Connect(ctx, url)
	require.NoError(t, err)
	defer conn.Close(ctx)

	var hasMetadata bool
	require.NoError(t, conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_namespace WHERE nspname = 'metadata')`).Scan(&hasMetadata))
	require.False(t, hasMetadata)
}

// CREATE DATABASE ... TEMPLATE drops `ALTER DATABASE ... SET` settings; testkit re-applies them,
// so a clone sees migration 0028's database-level search_path like a real migrated database.
func TestClonedDatabaseKeepsTheMigratedDatabaseLevelSearchPath(t *testing.T) {
	pool := testkit.NewPostgres(t)
	var searchPath string
	require.NoError(t, pool.QueryRow(context.Background(), `SHOW search_path`).Scan(&searchPath))
	require.Contains(t, searchPath, "metadata")
	require.Contains(t, searchPath, "control")
}
