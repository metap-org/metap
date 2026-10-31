// db-migrate applies migrations/*.sql — ported from metap's Rust metap-db-migrate
// binary. See the dbmigrate package's doc comment for why this tracks applied versions in its
// own table rather than sqlx's _sqlx_migrations.
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/metap-org/metap-go/dbmigrate"
	"github.com/metap-org/metap-go/infra"
)

func main() {
	infra.InitTracing()

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		slog.Error("DATABASE_URL is required")
		os.Exit(1)
	}

	migrationsDir := os.Getenv("MIGRATIONS_DIR")
	if migrationsDir == "" {
		// Relative to this binary's conventional invocation directory (the go/ module root,
		// e.g. `go run ./cmd/db-migrate` from go/) — migrations/ lives at the repo root,
		// alongside go/, not inside it.
		migrationsDir = "../migrations"
	}

	ctx := context.Background()
	slog.Info("connecting to postgres...")
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		slog.Error("connecting to postgres", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	slog.Info("applying migrations", "dir", migrationsDir)
	result, err := dbmigrate.Run(ctx, pool, migrationsDir)
	if err != nil {
		slog.Error("migration failed", "error", err)
		os.Exit(1)
	}

	slog.Info("done", "applied", result.Applied)
}
