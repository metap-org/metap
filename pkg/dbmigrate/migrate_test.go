package dbmigrate

import (
	"testing"

	"github.com/metap-org/metap/migrations"
	"github.com/stretchr/testify/require"
)

const realMigrationsDir = "../../migrations"

func TestLoadMigrationsParsesEveryRealMigrationFileInOrder(t *testing.T) {
	migrations, err := LoadMigrations(realMigrationsDir)
	require.NoError(t, err)
	require.False(t, len(migrations) < 30, "expected >= 30 real migration files, got %d", len(migrations))
	for i := 1; i < len(migrations); i++ {
		require.False(t, migrations[i].Version <= migrations[i-1].Version, "migrations not strictly increasing: %d then %d", migrations[i-1].Version, migrations[i].Version)
	}
}

// The embedded migrations.FS must carry exactly the on-disk migrations/ files — a guard
// against an embed pattern silently skipping a file.
func TestEmbeddedMigrationsMatchTheOnDiskDirectory(t *testing.T) {
	onDisk, err := LoadMigrations(realMigrationsDir)
	require.NoError(t, err)
	embedded, err := LoadMigrationsFS(migrations.FS)
	require.NoError(t, err)
	require.Len(t, embedded, len(onDisk), "embedded %d migrations, on disk %d", len(embedded), len(onDisk))
	for i := range onDisk {
		require.Equal(t, onDisk[i].Filename, embedded[i].Filename, "migration %d differs: embedded %s, on disk %s", i, embedded[i].Filename, onDisk[i].Filename)
		require.Equal(t, onDisk[i].Checksum, embedded[i].Checksum, "migration %d differs: embedded %s, on disk %s", i, embedded[i].Filename, onDisk[i].Filename)
	}
}
