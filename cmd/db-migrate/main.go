// db-migrate applies migrations/*.sql. See the dbmigrate package's doc comment for why this tracks
// applied versions in its own table rather than sqlx's _sqlx_migrations.
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/metap-org/metap/migrations"
	"github.com/metap-org/metap/pkg/config"
	"github.com/metap-org/metap/pkg/db/migrate"
	"github.com/metap-org/metap/pkg/kit/standard"
)

func main() {
	config.InitLogging()

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		slog.Error("DATABASE_URL is required")
		os.Exit(1)
	}

	ctx := context.Background()
	slog.Info("connecting to postgres...")
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		slog.Error("connecting to postgres", standard.LogError, err)
		os.Exit(1)
	}
	defer pool.Close()

	// Default: the migrations embedded into this binary (repo-root migrations package).
	// MIGRATIONS_DIR overrides with an on-disk directory.
	var result *migrate.Result
	if dir := os.Getenv("MIGRATIONS_DIR"); dir != "" {
		slog.Info("applying migrations", "dir", dir)
		result, err = migrate.Run(ctx, pool, dir)
	} else {
		slog.Info("applying embedded migrations")
		result, err = migrate.RunFS(ctx, pool, migrations.FS)
	}
	if err != nil {
		slog.Error("migration failed", standard.LogError, err)
		os.Exit(1)
	}

	slog.Info("done", "applied", result.Applied)
}
