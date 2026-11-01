package infra

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Connect opens a pooled Postgres connection — mirrors metap-infra's Rust connect.
func Connect(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, err
	}
	cfg.MaxConns = 5
	return pgxpool.NewWithConfig(ctx, cfg)
}

// ConnectForMigrate is a single-connection pool for running migrations, with search_path set at
// the connection level — see the Rust connect_for_migrate's doc comment for the exact bug this
// closes (ALTER DATABASE's new default search_path only applies to *future* connections, so the
// one connection a migration run keeps open must carry it from the start).
func ConnectForMigrate(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, err
	}
	cfg.MaxConns = 1
	cfg.ConnConfig.RuntimeParams["search_path"] = "public,metadata,control"
	return pgxpool.NewWithConfig(ctx, cfg)
}

// HealthCheck mirrors the Rust health_check.
func HealthCheck(ctx context.Context, pool *pgxpool.Pool) bool {
	_, err := pool.Exec(ctx, "select 1")
	return err == nil
}
