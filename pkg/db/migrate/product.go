// A product's own migrations: the tables of its entities, generated into SQL files it commits and
// reviews (`<service> migrate generate`). They have their own ledger, keyed by the product's name,
// because several products can share one database.

package migrate

import (
	"context"
	"fmt"
	"io/fs"

	"github.com/jackc/pgx/v5/pgxpool"
)

const productLedger = "metadata.metap_product_migrations"

// The ledger lives in the platform's metadata schema, so the platform's migrations run first.
const productLedgerDDL = `
CREATE TABLE IF NOT EXISTS metadata.metap_product_migrations (
	product text NOT NULL,
	version bigint NOT NULL,
	description text NOT NULL,
	checksum bytea NOT NULL,
	-- completed is false while a no-transaction migration has run only statements_done statements.
	completed boolean NOT NULL DEFAULT true,
	statements_done integer NOT NULL DEFAULT 0,
	installed_on timestamptz NOT NULL DEFAULT now(),
	PRIMARY KEY (product, version)
)`

// Product is one product's migrations.
type Product struct {
	Name string
	FS   fs.FS
}

type productRow struct {
	checksum  []byte
	completed bool
	done      int
}

func (p Product) rows(ctx context.Context, q querier) (map[int64]productRow, error) {
	out := map[int64]productRow{}
	ok, err := tableExists(ctx, q, productLedger)
	if err != nil || !ok {
		return out, err
	}
	rows, err := q.Query(ctx, "SELECT version, checksum, completed, statements_done FROM "+productLedger+" WHERE product = $1", p.Name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var v int64
		var r productRow
		if err := rows.Scan(&v, &r.checksum, &r.completed, &r.done); err != nil {
			return nil, err
		}
		out[v] = r
	}
	return out, rows.Err()
}

func (p Product) load() ([]Migration, error) {
	if p.Name == "" {
		return nil, fmt.Errorf("a product's migrations need its name")
	}
	if p.FS == nil {
		return nil, nil
	}
	return LoadMigrationsFS(p.FS)
}

func (p Product) state(ctx context.Context, q querier, migrations []Migration) (State, map[int64]productRow, error) {
	rows, err := p.rows(ctx, q)
	if err != nil {
		return State{}, nil, err
	}
	complete := map[int64][]byte{}
	var partial int64
	for v, r := range rows {
		if r.completed {
			complete[v] = r.checksum
		} else {
			partial = v
		}
	}
	s, err := stateOf(migrations, complete)
	if err != nil {
		return State{}, nil, err
	}
	for _, m := range s.Pending {
		if m.Version == partial {
			s.Partial, s.PartialDone = m, rows[partial].done
		}
	}
	return s, rows, nil
}

// State compares the product's migrations with the database. It only reads.
func (p Product) State(ctx context.Context, pool *pgxpool.Pool) (State, error) {
	migrations, err := p.load()
	if err != nil {
		return State{}, err
	}
	s, _, err := p.state(ctx, pool, migrations)
	return s, err
}

// Up applies every pending migration in order. A transactional one commits with its ledger row. A
// no-transaction one records each statement as it succeeds, and a later Up continues after the
// last recorded one.
func (p Product) Up(ctx context.Context, pool *pgxpool.Pool) ([]int64, error) {
	migrations, err := p.load()
	if err != nil {
		return nil, err
	}
	var applied []int64
	err = withLock(ctx, pool, func(conn *pgxpool.Conn) error {
		if _, err := conn.Exec(ctx, productLedgerDDL); err != nil {
			return fmt.Errorf("creating the ledger (are the platform migrations applied?): %w", err)
		}
		state, rows, err := p.state(ctx, conn, migrations)
		if err != nil {
			return err
		}
		for _, m := range state.Pending {
			if !NoTransaction(m.SQL) {
				if err := inTx(ctx, conn, m.SQL, "INSERT INTO "+productLedger+" (product, version, description, checksum) VALUES ($1, $2, $3, $4)",
					p.Name, m.Version, m.Description, m.Checksum[:]); err != nil {
					return fmt.Errorf("applying %s (nothing of it was kept): %w", m.Filename, err)
				}
				applied = append(applied, m.Version)
				continue
			}
			row, started := rows[m.Version]
			if started {
				if err := sameChecksum(m, row.checksum); err != nil {
					return err
				}
			} else if _, err := conn.Exec(ctx, "INSERT INTO "+productLedger+" (product, version, description, checksum, completed) VALUES ($1, $2, $3, $4, false)",
				p.Name, m.Version, m.Description, m.Checksum[:]); err != nil {
				return err
			}
			statements := Statements(m.SQL)
			for i := row.done; i < len(statements); i++ {
				if _, err := conn.Exec(ctx, statements[i]); err != nil {
					return fmt.Errorf("applying %s, statement %d of %d (the earlier ones are kept; fix the cause and run up again, or run down): %w",
						m.Filename, i+1, len(statements), err)
				}
				if _, err := conn.Exec(ctx, "UPDATE "+productLedger+" SET statements_done = $3 WHERE product = $1 AND version = $2", p.Name, m.Version, i+1); err != nil {
					return err
				}
			}
			if _, err := conn.Exec(ctx, "UPDATE "+productLedger+" SET completed = true, installed_on = now() WHERE product = $1 AND version = $2", p.Name, m.Version); err != nil {
				return err
			}
			applied = append(applied, m.Version)
		}
		return nil
	})
	return applied, err
}

// Down undoes the last steps migrations, newest first, a part-way one included. plan, when not
// nil, is given what is about to run and can refuse. A no-transaction down script runs statement
// by statement; until it finishes the migration counts as part-way, and running down again (or up)
// repeats statements, so they must tolerate that (generated ones do).
func (p Product) Down(ctx context.Context, pool *pgxpool.Pool, steps int, plan func([]Migration) error) ([]int64, error) {
	migrations, err := p.load()
	if err != nil {
		return nil, err
	}
	var done []int64
	err = withLock(ctx, pool, func(conn *pgxpool.Conn) error {
		rows, err := p.rows(ctx, conn)
		if err != nil {
			return err
		}
		recorded := map[int64][]byte{}
		for v, r := range rows {
			recorded[v] = r.checksum
		}
		targets, err := rollbackTargets(migrations, recorded, steps)
		if err != nil {
			return err
		}
		if plan != nil {
			if err := plan(targets); err != nil {
				return err
			}
		}
		const forget = "DELETE FROM " + productLedger + " WHERE product = $1 AND version = $2"
		for _, m := range targets {
			if !NoTransaction(m.Down) {
				if err := inTx(ctx, conn, m.Down, forget, p.Name, m.Version); err != nil {
					return fmt.Errorf("rolling back %s (nothing of it was undone): %w", m.Filename, err)
				}
			} else {
				// From here the migration is only partly in place: recorded as such first, so a failure
				// leaves it visible and neither status nor boot takes it for applied. Up then runs it
				// again from its first statement, down finishes undoing it.
				if _, err := conn.Exec(ctx, "UPDATE "+productLedger+" SET completed = false, statements_done = 0 WHERE product = $1 AND version = $2", p.Name, m.Version); err != nil {
					return err
				}
				for i, statement := range Statements(m.Down) {
					if _, err := conn.Exec(ctx, statement); err != nil {
						return fmt.Errorf("rolling back %s, statement %d: %w", m.Filename, i+1, err)
					}
				}
				if _, err := conn.Exec(ctx, forget, p.Name, m.Version); err != nil {
					return err
				}
			}
			done = append(done, m.Version)
		}
		return nil
	})
	return done, err
}

// Baseline records every pending migration as applied without running it, for a database whose
// tables already exist (one a service used to reconcile at boot). verify must confirm the database
// really has what the migrations would create; it may use the pool, so it runs before the lock is
// taken. Nothing is created until it passes.
func (p Product) Baseline(ctx context.Context, pool *pgxpool.Pool, verify func(context.Context) error) ([]int64, error) {
	migrations, err := p.load()
	if err != nil {
		return nil, err
	}
	unrecorded := func(q querier) (State, error) {
		state, rows, err := p.state(ctx, q, migrations)
		if err == nil && len(rows) > 0 {
			err = fmt.Errorf("%s already has migrations recorded: baseline is only for a database that has none", p.Name)
		}
		return state, err
	}
	if _, err := unrecorded(pool); err != nil {
		return nil, err
	}
	if err := verify(ctx); err != nil {
		return nil, err
	}
	var recorded []int64
	err = withLock(ctx, pool, func(conn *pgxpool.Conn) error {
		if _, err := conn.Exec(ctx, productLedgerDDL); err != nil {
			return fmt.Errorf("creating the ledger (are the platform migrations applied?): %w", err)
		}
		state, err := unrecorded(conn)
		if err != nil {
			return err
		}
		tx, err := conn.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck
		for _, m := range state.Pending {
			if _, err := tx.Exec(ctx, "INSERT INTO "+productLedger+" (product, version, description, checksum) VALUES ($1, $2, $3, $4)",
				p.Name, m.Version, m.Description, m.Checksum[:]); err != nil {
				return err
			}
			recorded = append(recorded, m.Version)
		}
		return tx.Commit(ctx)
	})
	return recorded, err
}
