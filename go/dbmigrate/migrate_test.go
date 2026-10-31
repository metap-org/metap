package dbmigrate

// Runs against a dedicated, disposable test database (never the shared `metap` database the
// Rust db-migrate tool and every other package's e2e test in this module depend on) — applying
// 38 real migration files from scratch is exactly the kind of destructive-by-nature operation
// that must never touch shared state.

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func testPool(t *testing.T) *pgxpool.Pool {
	url := os.Getenv("DB_MIGRATE_TEST_DATABASE_URL")
	if url == "" {
		url = "postgres://metap:metap@localhost:5432/metap_go_migrate_test"
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Skipf("no dedicated test database available (%v) — skipping", err)
	}
	if err := pool.Ping(context.Background()); err != nil {
		t.Skipf("no dedicated test database available (%v) — skipping", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

const realMigrationsDir = "../../migrations"

func TestLoadMigrationsParsesEveryRealMigrationFileInOrder(t *testing.T) {
	migrations, err := LoadMigrations(realMigrationsDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(migrations) < 30 {
		t.Fatalf("expected >= 30 real migration files, got %d", len(migrations))
	}
	for i := 1; i < len(migrations); i++ {
		if migrations[i].Version <= migrations[i-1].Version {
			t.Fatalf("migrations not strictly increasing: %d then %d", migrations[i-1].Version, migrations[i].Version)
		}
	}
}

func TestRunAppliesEveryMigrationFromScratchAndIsIdempotent(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	// Clean slate: drop everything so this test is repeatable across runs.
	if _, err := pool.Exec(ctx, "DROP SCHEMA IF EXISTS public, metadata, control, entities CASCADE; CREATE SCHEMA public;"); err != nil {
		t.Fatal(err)
	}

	migrations, err := LoadMigrations(realMigrationsDir)
	if err != nil {
		t.Fatal(err)
	}

	result, err := Run(ctx, pool, realMigrationsDir)
	if err != nil {
		t.Fatalf("first run failed: %v", err)
	}
	if len(result.Applied) != len(migrations) {
		t.Fatalf("expected all %d migrations applied, got %d", len(migrations), len(result.Applied))
	}

	// Idempotency: a second run against the now-fully-migrated database must apply nothing.
	result2, err := Run(ctx, pool, realMigrationsDir)
	if err != nil {
		t.Fatalf("second run failed: %v", err)
	}
	if len(result2.Applied) != 0 {
		t.Fatalf("expected a no-op second run, got %d newly applied", len(result2.Applied))
	}

	// Spot-check that real tables from early and late migrations actually exist.
	var count int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM metadata.users").Scan(&count); err != nil {
		t.Fatalf("metadata.users should exist after migrating: %v", err)
	}
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM metadata.audit_trail_entries").Scan(&count); err != nil {
		t.Fatalf("metadata.audit_trail_entries (a late migration) should exist: %v", err)
	}
}

// TestRunDetectsAnEditedAlreadyAppliedMigration is self-contained: it runs the real migrations
// to completion first (so there is a genuinely-applied row to corrupt), corrupts version 0's
// recorded checksum, confirms Run refuses to proceed, then restores the correct checksum so it
// doesn't leave the shared test database broken for whichever other test in this file runs next.
func TestRunDetectsAnEditedAlreadyAppliedMigration(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	migrations, err := LoadMigrations(realMigrationsDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Run(ctx, pool, realMigrationsDir); err != nil {
		t.Fatalf("priming run failed: %v", err)
	}

	correctChecksum := migrations[0].Checksum
	if _, err := pool.Exec(ctx, "UPDATE metap_go_migrations SET checksum = $1 WHERE version = $2",
		[]byte("0000000000000000000000000000000x"), migrations[0].Version); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "UPDATE metap_go_migrations SET checksum = $1 WHERE version = $2",
			correctChecksum[:], migrations[0].Version)
	})

	if _, err := Run(ctx, pool, realMigrationsDir); err == nil {
		t.Fatal("expected a checksum-mismatch error")
	}
}
