// db-migrate applies migrations/*.sql — ported from metap's Rust metap-db-migrate
// binary. See the dbmigrate package's doc comment for why this tracks applied versions in its
// own table rather than sqlx's _sqlx_migrations.
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/metap-org/metap/migrations"
	"github.com/metap-org/metap/pkg/dbmigrate"
	"github.com/metap-org/metap/pkg/infra"
)

func main() {
	infra.InitTracing()

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		slog.Error("DATABASE_URL is required")
		os.Exit(1)
	}

	ctx := context.Background()
	slog.Info("connecting to postgres...")
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		slog.Error("connecting to postgres", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	// Default: the migrations embedded into this binary (repo-root migrations package).
	// MIGRATIONS_DIR overrides with an on-disk directory.
	var result *dbmigrate.Result
	if dir := os.Getenv("MIGRATIONS_DIR"); dir != "" {
		slog.Info("applying migrations", "dir", dir)
		result, err = dbmigrate.Run(ctx, pool, dir)
	} else {
		slog.Info("applying embedded migrations")
		result, err = dbmigrate.RunFS(ctx, pool, migrations.FS)
	}
	if err != nil {
		slog.Error("migration failed", "error", err)
		os.Exit(1)
	}

	slog.Info("done", "applied", result.Applied)
}
