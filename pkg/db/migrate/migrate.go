// Package dbmigrate applies migrations/*.sql.
// This port deliberately does NOT write to that same _sqlx_migrations table: its checksum algorithm
// differs (SHA-256 here, since there is no public Go equivalent of sqlx's internal hash and
// reverse-engineering it isn't worth the risk), and inserting a row with a different checksum than
// sqlx itself would compute would make a later real `db-migrate` run against the same database fail
// its own checksum check. Tracking state lives in a separate table, metadata.metap_go_migrations,
// so the two tools can never collide. Only run this against a database this tool owns.
package migrate

import (
	"context"
	"fmt"
	"io/fs"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
)

const trackingTableDDL = `
CREATE TABLE IF NOT EXISTS metap_go_migrations (
	version BIGINT PRIMARY KEY,
	description TEXT NOT NULL,
	checksum BYTEA NOT NULL,
	installed_on TIMESTAMPTZ NOT NULL DEFAULT now()
)`

// sqlxPlaceholderTableDDL: migration 0030 runs `ALTER TABLE public._sqlx_migrations SET SCHEMA
// metadata` — a real statement in the shared migrations directory, not something either port can
// skip, since it's the one place the `records`/`attachments` table relocation in that same file
// happens. The real sqlx tool always creates `_sqlx_migrations` itself before running any user
// migration. This port isn't sqlx and never populates this table with real bookkeeping rows, but it
// still needs to exist as a placeholder so the same shared SQL file succeeds unmodified — found
// live: without this, migration 0030 fails outright (`relation "_sqlx_migrations" does not exist`),
// the one real migrations-directory coupling to sqlx found while verifying this tool against a real
// database from scratch.
const sqlxPlaceholderTableDDL = `CREATE TABLE IF NOT EXISTS public._sqlx_migrations (version BIGINT PRIMARY KEY)`

// Result is what Run reports.
type Result struct {
	Applied []int64 // versions applied this run, in order
}

// Run applies every migration in dir not yet recorded in this database's own tracking table,
// in version order, each inside its own transaction (one per file, not one for the batch, so a
// failure partway through leaves every earlier file's migration applied).
func Run(ctx context.Context, pool *pgxpool.Pool, dir string) (*Result, error) {
	return RunFS(ctx, pool, os.DirFS(dir))
}

// RunFS is Run over any fs.FS (see LoadMigrationsFS).
func RunFS(ctx context.Context, pool *pgxpool.Pool, fsys fs.FS) (*Result, error) {
	migrations, err := LoadMigrationsFS(fsys)
	if err != nil {
		return nil, err
	}
	result := &Result{}
	err = withLock(ctx, pool, func(conn *pgxpool.Conn) error {
		if _, err := conn.Exec(ctx, trackingTableDDL); err != nil {
			return err
		}
		if _, err := conn.Exec(ctx, sqlxPlaceholderTableDDL); err != nil {
			return err
		}
		applied, err := platformApplied(ctx, conn)
		if err != nil {
			return err
		}
		for _, m := range migrations {
			if existing, ok := applied[m.Version]; ok {
				if err := sameChecksum(m, existing); err != nil {
					return err
				}
				continue
			}
			if NoTransaction(m.SQL) {
				return fmt.Errorf("migration %s: the platform's migrations are transactional", m.Filename)
			}
			if err := inTx(ctx, conn, m.SQL, "INSERT INTO metap_go_migrations (version, description, checksum) VALUES ($1, $2, $3)",
				m.Version, m.Description, m.Checksum[:]); err != nil {
				return fmt.Errorf("applying migration %s: %w", m.Filename, err)
			}
			result.Applied = append(result.Applied, m.Version)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// Rollback undoes the last steps applied platform migrations, newest first, each in its own
// transaction. It checks them all first: one without a down script (every migration up to
// PlatformBaseline) or newer than this build stops it before anything runs. plan, when not nil, is
// given what is about to run and can refuse.
func Rollback(ctx context.Context, pool *pgxpool.Pool, fsys fs.FS, steps int, plan func([]Migration) error) ([]int64, error) {
	migrations, err := LoadMigrationsFS(fsys)
	if err != nil {
		return nil, err
	}
	var done []int64
	err = withLock(ctx, pool, func(conn *pgxpool.Conn) error {
		applied, err := platformApplied(ctx, conn)
		if err != nil {
			return err
		}
		targets, err := rollbackTargets(migrations, applied, steps)
		if err != nil {
			return err
		}
		if plan != nil {
			if err := plan(targets); err != nil {
				return err
			}
		}
		for _, m := range targets {
			if err := inTx(ctx, conn, m.Down, "DELETE FROM metap_go_migrations WHERE version = $1", m.Version); err != nil {
				return fmt.Errorf("rolling back %s: %w", m.Filename, err)
			}
			done = append(done, m.Version)
		}
		return nil
	})
	return done, err
}

// PlatformBaseline is the last platform migration written before down scripts were required. It
// and everything before it cannot be rolled back; restore a backup instead.
const PlatformBaseline = 61

func platformApplied(ctx context.Context, q querier) (map[int64][]byte, error) {
	return appliedChecksums(ctx, q, "SELECT version, checksum FROM metap_go_migrations")
}
