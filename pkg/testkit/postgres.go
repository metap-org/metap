// Package testkit is the shared harness for integration tests against real infrastructure, for
// this repo and for downstream repos built on it (e.g. ../waap).
//
// Integration test files carry `//go:build integration` and run with
// `go test -tags integration ./...`. Every helper here FAILS the test when the infrastructure it
// needs is unreachable — it never skips. A plain `go test ./...` (no tag) never compiles those
// files, so it runs only unit tests; and an integration run that silently tested nothing can no
// longer look green.
//
// Isolation: NewPostgres gives every caller its own throwaway database, cloned from a template
// database that has the real embedded migrations (migrations.FS) applied. Tests never share
// rows, never depend on a developer's dev database state, and can run in parallel.
package testkit

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/metap-org/metap/migrations"
	"github.com/metap-org/metap/pkg/dbmigrate"
)

// DefaultDatabaseURL matches the repo's docker-compose.yml postgres service (host port 5433).
const DefaultDatabaseURL = "postgres://metap:metap@localhost:5433/metap"

// templateLockKey serializes template creation across concurrently running test binaries
// (`go test ./...` runs packages in parallel processes).
const templateLockKey int64 = 0x6d65746170746b // "metaptk"

// BaseDatabaseURL is the server testkit creates its databases on: METAP_TEST_DATABASE_URL, else
// DATABASE_URL, else DefaultDatabaseURL. The role must be allowed to CREATE DATABASE. Its own
// database is only used as a maintenance connection — never written to.
func BaseDatabaseURL() string {
	if v := os.Getenv("METAP_TEST_DATABASE_URL"); v != "" {
		return v
	}
	if v := os.Getenv("DATABASE_URL"); v != "" {
		return v
	}
	return DefaultDatabaseURL
}

var (
	templateOnce sync.Once
	templateName string
	templateErr  error
)

// Option adjusts the pool NewPostgres returns.
type Option func(*pgxpool.Config)

// WithSearchPath sets search_path on every pooled connection, e.g. WithSearchPath("metadata",
// "public") for code that queries unqualified metadata.* tables.
func WithSearchPath(schemas ...string) Option {
	return func(cfg *pgxpool.Config) {
		ids := make([]string, len(schemas))
		for i, s := range schemas {
			ids[i] = pgx.Identifier{s}.Sanitize()
		}
		cfg.ConnConfig.RuntimeParams["search_path"] = strings.Join(ids, ", ")
	}
}

// NewPostgres returns a pool on a fresh, fully migrated database private to this test, dropped
// on cleanup.
func NewPostgres(t testing.TB, opts ...Option) *pgxpool.Pool {
	t.Helper()
	return connect(t, NewDatabaseURL(t), opts...)
}

// NewDatabaseURL creates a fresh, fully migrated database private to this test and returns its
// URL — for code under test that opens its own connections. Dropped on cleanup.
func NewDatabaseURL(t testing.TB) string {
	t.Helper()
	templateOnce.Do(func() { templateName, templateErr = ensureTemplate(context.Background()) })
	if templateErr != nil {
		t.Fatalf("testkit: preparing migrated template database on %s: %v", redact(BaseDatabaseURL()), templateErr)
	}
	return createDatabase(t, templateName)
}

// NewEmptyDatabaseURL creates a fresh database with no migrations applied (cloned from
// template1) — for tests of migration/provisioning code itself. Dropped on cleanup.
func NewEmptyDatabaseURL(t testing.TB) string {
	t.Helper()
	return createDatabase(t, "template1")
}

func createDatabase(t testing.TB, template string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	admin, err := pgx.Connect(ctx, BaseDatabaseURL())
	if err != nil {
		t.Fatalf(
			"testkit: no Postgres at %s (start it with `docker compose up -d postgres`, or set METAP_TEST_DATABASE_URL): %v",
			redact(BaseDatabaseURL()),
			err,
		)
	}
	defer admin.Close(ctx)

	name := "metap_t_" + randomHex(8)
	if _, err := admin.Exec(ctx, fmt.Sprintf(`CREATE DATABASE %s TEMPLATE %s`, pgx.Identifier{name}.Sanitize(), pgx.Identifier{template}.Sanitize())); err != nil {
		t.Fatalf("testkit: creating database %s from %s: %v", name, template, err)
	}
	if err := copyDatabaseSettings(ctx, admin, template, name); err != nil {
		t.Fatalf("testkit: copying database-level settings from %s to %s: %v", template, name, err)
	}
	t.Cleanup(func() {
		if os.Getenv("METAP_TEST_KEEP_DB") != "" {
			t.Logf("testkit: keeping database %s (METAP_TEST_KEEP_DB set)", name)
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		admin, err := pgx.Connect(ctx, BaseDatabaseURL())
		if err != nil {
			t.Logf("testkit: could not drop database %s: %v", name, err)
			return
		}
		defer admin.Close(ctx)
		if _, err := admin.Exec(ctx, fmt.Sprintf(`DROP DATABASE IF EXISTS %s WITH (FORCE)`, pgx.Identifier{name}.Sanitize())); err != nil {
			t.Logf("testkit: could not drop database %s: %v", name, err)
		}
	})
	return withDatabase(BaseDatabaseURL(), name)
}

// copyDatabaseSettings re-applies the source database's `ALTER DATABASE ... SET` settings to dst.
// CREATE DATABASE ... TEMPLATE copies schema and data but not these (they live in the shared
// pg_db_role_setting catalog, keyed by database oid) — and migration 0028 relies on one
// (search_path = public, metadata, control) for every unqualified metadata.*/control.* query.
func copyDatabaseSettings(ctx context.Context, admin *pgx.Conn, src, dst string) error {
	rows, err := admin.Query(ctx, `
		SELECT unnest(s.setconfig) FROM pg_db_role_setting s
		JOIN pg_database d ON d.oid = s.setdatabase
		WHERE d.datname = $1 AND s.setrole = 0`, src)
	if err != nil {
		return err
	}
	settings, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	for _, kv := range settings {
		key, value, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		// pg_db_role_setting stores the flattened text form; a list setting like search_path
		// must be re-applied unquoted (quoting would make it a single element).
		stmt := fmt.Sprintf(
			`ALTER DATABASE %s SET %s TO %s`,
			pgx.Identifier{dst}.Sanitize(),
			pgx.Identifier{key}.Sanitize(),
			value,
		)
		if _, err := admin.Exec(ctx, stmt); err != nil {
			quoted := fmt.Sprintf(
				`ALTER DATABASE %s SET %s TO '%s'`,
				pgx.Identifier{dst}.Sanitize(),
				pgx.Identifier{key}.Sanitize(),
				strings.ReplaceAll(value, "'", "''"),
			)
			if _, err := admin.Exec(ctx, quoted); err != nil {
				return fmt.Errorf("%s: %w", kv, err)
			}
		}
	}
	return nil
}

func connect(t testing.TB, databaseURL string, opts ...Option) *pgxpool.Pool {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatalf("testkit: parsing %s: %v", redact(databaseURL), err)
	}
	for _, opt := range opts {
		opt(cfg)
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("testkit: connecting to %s: %v", redact(databaseURL), err)
	}
	if err := pool.Ping(context.Background()); err != nil {
		pool.Close()
		t.Fatalf("testkit: pinging %s: %v", redact(databaseURL), err)
	}
	// Registered after createDatabase's DROP, so it runs first (Cleanup is LIFO).
	t.Cleanup(pool.Close)
	return pool
}

// ensureTemplate returns the name of a template database with every embedded migration applied,
// creating it if needed. The name is derived from the migrations' checksums, so editing or adding
// a migration yields a new template automatically. It is built under a scratch name and renamed
// only once fully migrated, so a crashed run never leaves a half-migrated template behind.
func ensureTemplate(ctx context.Context) (string, error) {
	all, err := dbmigrate.LoadMigrationsFS(migrations.FS)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	for _, m := range all {
		h.Write([]byte(m.Filename))
		h.Write(m.Checksum[:])
	}
	name := "metap_tpl_" + hex.EncodeToString(h.Sum(nil))[:12]

	admin, err := pgx.Connect(ctx, BaseDatabaseURL())
	if err != nil {
		return "", fmt.Errorf(
			"no Postgres (start it with `docker compose up -d postgres`, or set METAP_TEST_DATABASE_URL): %w",
			err,
		)
	}
	defer admin.Close(ctx)
	if _, err := admin.Exec(ctx, `SELECT pg_advisory_lock($1)`, templateLockKey); err != nil {
		return "", err
	}
	defer admin.Exec(ctx, `SELECT pg_advisory_unlock($1)`, templateLockKey) //nolint:errcheck

	var exists bool
	if err := admin.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`, name).Scan(&exists); err != nil {
		return "", err
	}
	if exists {
		return name, nil
	}

	scratch := pgx.Identifier{name + "_building"}.Sanitize()
	if _, err := admin.Exec(ctx, `DROP DATABASE IF EXISTS `+scratch+` WITH (FORCE)`); err != nil {
		return "", err
	}
	if _, err := admin.Exec(ctx, `CREATE DATABASE `+scratch+` TEMPLATE template1`); err != nil {
		return "", err
	}
	pool, err := pgxpool.New(ctx, withDatabase(BaseDatabaseURL(), name+"_building"))
	if err != nil {
		return "", err
	}
	_, migrateErr := dbmigrate.RunFS(ctx, pool, migrations.FS)
	pool.Close()
	if migrateErr != nil {
		admin.Exec(ctx, `DROP DATABASE IF EXISTS `+scratch+` WITH (FORCE)`) //nolint:errcheck
		return "", fmt.Errorf("applying migrations to template: %w", migrateErr)
	}
	if _, err := admin.Exec(ctx, `ALTER DATABASE `+scratch+` RENAME TO `+pgx.Identifier{name}.Sanitize()); err != nil {
		return "", err
	}
	if _, err := admin.Exec(ctx, `ALTER DATABASE `+pgx.Identifier{name}.Sanitize()+` IS_TEMPLATE true`); err != nil {
		return "", err
	}
	return name, nil
}

func withDatabase(databaseURL, name string) string {
	u, err := url.Parse(databaseURL)
	if err != nil {
		return databaseURL
	}
	u.Path = "/" + name
	return u.String()
}

func redact(databaseURL string) string {
	u, err := url.Parse(databaseURL)
	if err != nil {
		return "<unparseable database URL>"
	}
	return u.Redacted()
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
