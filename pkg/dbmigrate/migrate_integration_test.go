//go:build integration

// Applying every real migration from scratch needs a database nothing else uses — testkit
// creates a fresh, empty one per test, so this never touches a developer's own database.
package dbmigrate_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/dbmigrate"
	"github.com/metap-org/metap/pkg/testkit"
)

const realMigrationsDir = "../../migrations"

func emptyDatabase(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), testkit.NewEmptyDatabaseURL(t))
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool
}

func TestRunAppliesEveryMigrationFromScratchAndIsIdempotent(t *testing.T) {
	pool := emptyDatabase(t)
	ctx := context.Background()

	// Clean slate: drop everything so this test is repeatable across runs.

	migrations, err := dbmigrate.LoadMigrations(realMigrationsDir)
	require.NoError(t, err)

	result, err := dbmigrate.Run(ctx, pool, realMigrationsDir)
	require.NoError(t, err, "first run failed")
	require.Len(t, result.Applied, len(migrations), "expected all %d migrations applied, got %d", len(migrations), len(result.Applied))

	// Idempotency: a second run against the now-fully-migrated database must apply nothing.
	result2, err := dbmigrate.Run(ctx, pool, realMigrationsDir)
	require.NoError(t, err, "second run failed")
	require.Len(t, result2.Applied, 0, "expected a no-op second run, got %d newly applied", len(result2.Applied))

	// Spot-check that real tables from early and late migrations actually exist.
	var count int
	require.NoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM metadata.users").Scan(&count), "metadata.users should exist after migrating")
	require.NoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM metadata.audit_trail_entries").Scan(&count), "metadata.audit_trail_entries (a late migration) should exist")
}

// TestRunDetectsAnEditedAlreadyAppliedMigration is self-contained: it runs the real migrations
// to completion first (so there is a genuinely-applied row to corrupt), corrupts version 0's
// recorded checksum, confirms Run refuses to proceed, then restores the correct checksum so it
// doesn't leave the shared test database broken for whichever other test in this file runs next.
func TestRunDetectsAnEditedAlreadyAppliedMigration(t *testing.T) {
	pool := emptyDatabase(t)
	ctx := context.Background()

	migrations, err := dbmigrate.LoadMigrations(realMigrationsDir)
	require.NoError(t, err)
	_, err = dbmigrate.Run(ctx, pool, realMigrationsDir)
	require.NoError(t, err, "priming run failed")

	correctChecksum := migrations[0].Checksum
	_, err = pool.Exec(ctx, "UPDATE metap_go_migrations SET checksum = $1 WHERE version = $2",
		[]byte("0000000000000000000000000000000x"), migrations[0].Version)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "UPDATE metap_go_migrations SET checksum = $1 WHERE version = $2",
			correctChecksum[:], migrations[0].Version)
	})

	_, err = dbmigrate.Run(ctx, pool, realMigrationsDir)
	require.Error(t, err, "expected a checksum-mismatch error")
}
