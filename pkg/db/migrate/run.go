package migrate

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// withLock runs fn on one connection holding the migration lock, so two runs never overlap.
// Everything fn does goes through that connection: a pool of one is enough.
func withLock(ctx context.Context, pool *pgxpool.Pool, fn func(conn *pgxpool.Conn) error) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock(hashtext('metap.migrate')::bigint)"); err != nil {
		return err
	}
	defer func() {
		_, _ = conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock(hashtext('metap.migrate')::bigint)")
	}()
	return fn(conn)
}

// inTx runs script and the ledger statement that records it in one transaction.
func inTx(ctx context.Context, conn *pgxpool.Conn, script, ledger string, args ...any) error {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck
	if _, err := tx.Exec(ctx, script); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, ledger, args...); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func appliedChecksums(ctx context.Context, q querier, sql string, args ...any) (map[int64][]byte, error) {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]byte{}
	for rows.Next() {
		var version int64
		var checksum []byte
		if err := rows.Scan(&version, &checksum); err != nil {
			return nil, err
		}
		out[version] = checksum
	}
	return out, rows.Err()
}

func sameChecksum(m Migration, applied []byte) error {
	if !bytes.Equal(m.Checksum[:], applied) {
		return fmt.Errorf("migration %s: checksum mismatch against the already-applied version — "+
			"the file was edited after it was applied", m.Filename)
	}
	return nil
}

// rollbackTargets is the last steps applied versions, newest first, each with a down script.
func rollbackTargets(migrations []Migration, applied map[int64][]byte, steps int) ([]Migration, error) {
	if steps < 1 {
		return nil, fmt.Errorf("rollback needs at least 1 step, got %d", steps)
	}
	known := map[int64]Migration{}
	for _, m := range migrations {
		known[m.Version] = m
	}
	versions := make([]int64, 0, len(applied))
	for v := range applied {
		versions = append(versions, v)
	}
	sort.Slice(versions, func(i, j int) bool { return versions[i] > versions[j] })
	var out []Migration
	for _, v := range versions[:min(steps, len(versions))] {
		m, ok := known[v]
		if !ok {
			return nil, fmt.Errorf("version %d is applied but this build does not have it: roll back with the build that applied it", v)
		}
		if m.Down == "" {
			return nil, fmt.Errorf("%s has no down script and cannot be rolled back", m.Filename)
		}
		if err := sameChecksum(m, applied[v]); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}

// State is where a set of migrations stands against a database.
type State struct {
	// Applied are the versions recorded as complete, ascending.
	Applied []int64
	// Pending are this build's migrations not applied yet, ascending.
	Pending []Migration
	// Partial is a no-transaction migration that stopped part-way (version 0: none).
	Partial Migration
	// PartialDone is how many of Partial's statements ran.
	PartialDone int
	// Unknown are applied versions this build does not have: the database is ahead of it.
	Unknown []int64
}

func stateOf(migrations []Migration, applied map[int64][]byte) (State, error) {
	var s State
	known := map[int64]bool{}
	for _, m := range migrations {
		known[m.Version] = true
		checksum, ok := applied[m.Version]
		if !ok {
			s.Pending = append(s.Pending, m)
			continue
		}
		if err := sameChecksum(m, checksum); err != nil {
			return s, err
		}
		s.Applied = append(s.Applied, m.Version)
	}
	for v := range applied {
		if !known[v] {
			s.Unknown = append(s.Unknown, v)
		}
	}
	sort.Slice(s.Unknown, func(i, j int) bool { return s.Unknown[i] < s.Unknown[j] })
	return s, nil
}

func tableExists(ctx context.Context, q querier, name string) (bool, error) {
	var ok bool
	err := q.QueryRow(ctx, "SELECT to_regclass($1) IS NOT NULL", name).Scan(&ok)
	return ok, err
}

// PlatformState compares fsys with what the database has applied. It only reads: a database
// that was never migrated has everything pending.
func PlatformState(ctx context.Context, pool *pgxpool.Pool, fsys fs.FS) (State, error) {
	migrations, err := LoadMigrationsFS(fsys)
	if err != nil {
		return State{}, err
	}
	ok, err := tableExists(ctx, pool, "metap_go_migrations")
	if err != nil {
		return State{}, err
	}
	applied := map[int64][]byte{}
	if ok {
		if applied, err = platformApplied(ctx, pool); err != nil {
			return State{}, err
		}
	}
	return stateOf(migrations, applied)
}
