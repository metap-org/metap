// Package dbmigrate applies crates/migrations/*.sql — ported from metap's Rust metap-db-migrate
// binary. The Rust version is a thin wrapper around sqlx::migrate!, a compile-time proc macro
// that embeds the migrations directory and tracks applied versions in its own _sqlx_migrations
// table (SHA-384 checksums, a private sqlx implementation detail).
//
// This port deliberately does NOT write to that same _sqlx_migrations table: its checksum
// algorithm differs (SHA-256 here, since there is no public Go equivalent of sqlx's internal
// hash and reverse-engineering it isn't worth the risk), and inserting a row with a different
// checksum than sqlx itself would compute would make a later real `db-migrate` run against the
// same database fail its own checksum check — corrupting the Rust tool's migration state for a
// database this port never owns exclusively. Tracking state lives in a separate table,
// metadata.metap_go_migrations, so the two tools can never collide. This means a database
// previously migrated only by the Rust tool looks completely unmigrated to this one — by
// design: these are a parallel rewrite's own tracking tables, not meant to interoperate. Only
// run this against a database this Go port owns, never one the Rust `db-migrate`/the live
// system is also managing.
package dbmigrate

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// migration is one parsed crates/migrations/NNNN_description.sql file.
type migration struct {
	Version     int64
	Description string
	Filename    string
	SQL         string
	Checksum    [32]byte
}

var filenamePattern = regexp.MustCompile(`^(\d+)_(.+)\.sql$`)

// LoadMigrations reads and parses every *.sql file in dir, sorted by version ascending.
func LoadMigrations(dir string) ([]migration, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []migration
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		m := filenamePattern.FindStringSubmatch(entry.Name())
		if m == nil {
			continue
		}
		version, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("migration %s: invalid version prefix: %w", entry.Name(), err)
		}
		content, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		out = append(out, migration{
			Version: version, Description: strings.ReplaceAll(m[2], "_", " "),
			Filename: entry.Name(), SQL: string(content), Checksum: sha256.Sum256(content),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}

const trackingTableDDL = `
CREATE TABLE IF NOT EXISTS metap_go_migrations (
	version BIGINT PRIMARY KEY,
	description TEXT NOT NULL,
	checksum BYTEA NOT NULL,
	installed_on TIMESTAMPTZ NOT NULL DEFAULT now()
)`

// sqlxPlaceholderTableDDL: migration 0030 runs `ALTER TABLE public._sqlx_migrations SET SCHEMA
// metadata` — a real statement in the shared migrations directory, not something either port
// can skip, since it's the one place the `records`/`attachments` table relocation in that same
// file happens. The real sqlx tool always creates `_sqlx_migrations` itself before running any
// user migration, which is what makes that statement valid when the Rust db-migrate runs this
// file. This port isn't sqlx and never populates this table with real bookkeeping rows, but it
// still needs to exist as a placeholder so the same shared SQL file succeeds unmodified — found
// live: without this, migration 0030 fails outright (`relation "_sqlx_migrations" does not
// exist`), the one real migrations-directory coupling to sqlx found while verifying this port
// against a real database from scratch.
const sqlxPlaceholderTableDDL = `CREATE TABLE IF NOT EXISTS public._sqlx_migrations (version BIGINT PRIMARY KEY)`

// Result is what Run reports.
type Result struct {
	Applied []int64 // versions applied this run, in order
}

// Run applies every migration in dir not yet recorded in this database's own tracking table,
// in version order, each inside its own transaction — mirrors sqlx::migrate!().run()'s
// behavior (one transaction per migration file, not one for the whole batch, so a failure
// partway through leaves every earlier file's migration durably applied).
func Run(ctx context.Context, pool *pgxpool.Pool, dir string) (*Result, error) {
	migrations, err := LoadMigrations(dir)
	if err != nil {
		return nil, err
	}

	if _, err := pool.Exec(ctx, trackingTableDDL); err != nil {
		return nil, err
	}
	if _, err := pool.Exec(ctx, sqlxPlaceholderTableDDL); err != nil {
		return nil, err
	}

	applied := map[int64][32]byte{}
	rows, err := pool.Query(ctx, "SELECT version, checksum FROM metap_go_migrations")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var version int64
		var checksum []byte
		if err := rows.Scan(&version, &checksum); err != nil {
			rows.Close()
			return nil, err
		}
		var c [32]byte
		copy(c[:], checksum)
		applied[version] = c
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	result := &Result{}
	for _, m := range migrations {
		if existing, ok := applied[m.Version]; ok {
			if existing != m.Checksum {
				return nil, fmt.Errorf("migration %s: checksum mismatch against the already-applied version — "+
					"the file was edited after it was applied", m.Filename)
			}
			continue
		}

		tx, err := pool.Begin(ctx)
		if err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, m.SQL); err != nil {
			tx.Rollback(ctx)
			return nil, fmt.Errorf("applying migration %s: %w", m.Filename, err)
		}
		if _, err := tx.Exec(ctx, "INSERT INTO metap_go_migrations (version, description, checksum) VALUES ($1, $2, $3)",
			m.Version, m.Description, m.Checksum[:]); err != nil {
			tx.Rollback(ctx)
			return nil, err
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, err
		}
		result.Applied = append(result.Applied, m.Version)
	}
	return result, nil
}
