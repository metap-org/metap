package infra

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Connect opens a pooled Postgres connection.
func Connect(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, err
	}
	// pool_max_conns in the URL (pgx's own parameter) sizes the pool; without it the default applies.
	// The default stays small on purpose, but a service that has to carry real load can no longer be
	// stuck at it: every request costs several short queries, so the pool is the throughput ceiling.
	if poolSizeSet(databaseURL) {
		return pgxpool.NewWithConfig(ctx, cfg)
	}
	cfg.MaxConns = defaultMaxConns
	return pgxpool.NewWithConfig(ctx, cfg)
}

// ConnectForMigrate is a single-connection pool for running migrations, with search_path set at the
// connection level.
func ConnectForMigrate(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, err
	}
	cfg.MaxConns = 1
	cfg.ConnConfig.RuntimeParams["search_path"] = "public,metadata,control"
	return pgxpool.NewWithConfig(ctx, cfg)
}

func HealthCheck(ctx context.Context, pool *pgxpool.Pool) bool {
	_, err := pool.Exec(ctx, "select 1")
	return err == nil
}

// defaultMaxConns is the pool size of a service's main Postgres pool.
const defaultMaxConns = 5

// poolSizeSet reports whether the URL (or DSN) names a pool size itself.
func poolSizeSet(databaseURL string) bool {
	return strings.Contains(databaseURL, "pool_max_conns=")
}
