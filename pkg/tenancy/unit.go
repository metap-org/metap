package tenancy

import (
	"context"
	"errors"
	"sync"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// A unit of work is one transaction on a tenant's database that everything called with its context
// joins (roadmap 120, P3). Router.Begin inside it returns a savepoint on the unit's transaction
// instead of a new connection, so record writes, policy reads and outbox events of one piece of
// work commit or roll back together, on ONE connection: a pool of one still serves it, which is
// the rule the integration suite enforces (no path holds a connection while it needs another).
//
// What is not routed through Router.Begin (the audit store, which keeps its own hash chain, and
// anything using a pool directly) is not part of the unit: register it with OnCommit so it runs
// after the commit, or does not run when the unit rolls back.

type unitKey struct{}

// Unit is an open unit of work. Commit or Rollback it exactly once.
type Unit struct {
	tenant uuid.UUID
	tx     pgx.Tx
	// sharedDB is true when the unit's database is the shared one (a Schema-strategy tenant), where the
	// identity tables live too, so identity statements can run on the unit's own transaction.
	sharedDB bool

	mu    sync.Mutex
	hooks []func()
	done  bool
}

// ErrUnitOpen is returned by BeginUnit when ctx already carries a unit of work: units do not nest.
var ErrUnitOpen = errors.New("tenancy: a unit of work is already open in this context")

// BeginUnit opens a transaction on tenantID's database and returns a context whose Router.Begin
// calls for that tenant join it.
func (r *Router) BeginUnit(ctx context.Context, tenantID uuid.UUID) (context.Context, *Unit, error) {
	if unitFrom(ctx) != nil {
		return ctx, nil, ErrUnitOpen
	}
	// Resolved before the connection is taken: the registry lookup may need a connection of its own, and
	// on a pool of one that would wait for the one the unit now holds.
	strategy, err := r.resolve(ctx, tenantID)
	if err != nil {
		return ctx, nil, err
	}
	tx, err := r.begin(ctx, tenantID)
	if err != nil {
		return ctx, nil, err
	}
	u := &Unit{tenant: tenantID, tx: tx, sharedDB: strategy.Kind == StrategySchema}
	return context.WithValue(ctx, unitKey{}, u), u, nil
}

// BeginSharedUnit opens a unit of work on the shared database whatever a tenant's strategy is: for
// work that belongs to the identity data (members, keys, sessions), which lives there even for a tenant
// with a database of its own. Its outbox rows are drained by the same worker that drains a tenant's, so
// an audit entry written in it reaches the one audit store. It joins no tenant's Router.Begin (its
// tenant is the nil id), and SharedTx returns its transaction.
func (r *Router) BeginSharedUnit(ctx context.Context) (context.Context, *Unit, error) {
	if unitFrom(ctx) != nil {
		return ctx, nil, ErrUnitOpen
	}
	tx, err := r.sharedPool.Begin(ctx)
	if err != nil {
		return ctx, nil, err
	}
	u := &Unit{tenant: uuid.Nil, tx: tx, sharedDB: true}
	return context.WithValue(ctx, unitKey{}, u), u, nil
}

func unitFrom(ctx context.Context) *Unit {
	u, _ := ctx.Value(unitKey{}).(*Unit)
	return u
}

// SharedTx is the transaction of the unit of work in ctx when that unit runs on the shared database,
// and nil otherwise (no unit, or a tenant with a database of its own). The shared identity tables live
// in that database, so code that writes them (pkg/auth/identity) joins the unit through it: the
// identity change and the tenant's records commit or roll back together, on one connection. For a
// tenant with a database of its own the identity data is elsewhere and cannot join the unit; the
// caller then uses the shared pool, which is a different connection and a different transaction.
//
// It is the unit's transaction itself, not a savepoint: a statement that fails (a unique violation
// the caller means to handle) aborts the unit. Code that expects such an error opens its own
// savepoint with Begin on the transaction first.
func SharedTx(ctx context.Context) pgx.Tx {
	if u := unitFrom(ctx); u != nil && u.sharedDB {
		return u.tx
	}
	return nil
}

// Tx is the unit's transaction, for code that writes to it directly (the outbox enqueue).
func (u *Unit) Tx() pgx.Tx { return u.tx }

// Commit commits the unit, then runs what was registered with OnCommit, in order.
func (u *Unit) Commit(ctx context.Context) error {
	hooks, err := u.finish(func() error { return u.tx.Commit(ctx) })
	if err != nil {
		return err
	}
	for _, h := range hooks {
		h()
	}
	return nil
}

// Rollback rolls the unit back and drops what was registered with OnCommit. Safe after Commit.
func (u *Unit) Rollback(ctx context.Context) {
	_, _ = u.finish(func() error { return u.tx.Rollback(ctx) })
}

// finish ends the unit once and hands back the hooks to run when it ended by committing.
func (u *Unit) finish(end func() error) ([]func(), error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.done {
		return nil, nil
	}
	u.done = true
	hooks := u.hooks
	u.hooks = nil
	if err := end(); err != nil {
		return nil, err
	}
	return hooks, nil
}

// OnCommit runs fn once the unit of work in ctx commits, and never if it rolls back. With no unit
// in ctx there is nothing to wait for and fn runs now, so a caller writes the same code in both
// cases.
func OnCommit(ctx context.Context, fn func()) {
	u := unitFrom(ctx)
	if u == nil {
		fn()
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.done {
		return
	}
	u.hooks = append(u.hooks, fn)
}

// WithKeyLock runs fn while holding a database lock named key, so two callers using the same key run
// one after the other, across goroutines and across processes. Use it for a read-then-write that
// must not interleave with itself (count the rows, then add one under a limit).
//
// The lock is a transaction-level advisory lock, taken on the transaction fn's work joins and
// released when that transaction ends. Inside a unit of work for this tenant it is that unit's
// transaction, so the lock is held until the unit commits and fn's reads and writes (made with the
// context fn is given) are on the same connection. Outside one, a unit of its own is opened around
// fn and committed when fn returns nil. Either way it needs no second connection while it waits.
//
// key should name the tenant and the thing being serialised; it is not interpreted.
func (r *Router) WithKeyLock(ctx context.Context, tenantID uuid.UUID, key string, fn func(ctx context.Context) error) error {
	const lock = "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))"
	if u := unitFrom(ctx); u != nil && u.tenant == tenantID {
		if _, err := u.tx.Exec(ctx, lock, key); err != nil {
			return err
		}
		return fn(ctx)
	}
	uctx, u, err := r.BeginUnit(ctx, tenantID)
	if err != nil {
		return err
	}
	if _, err := u.tx.Exec(uctx, lock, key); err != nil {
		u.Rollback(ctx)
		return err
	}
	if err := fn(uctx); err != nil {
		u.Rollback(ctx)
		return err
	}
	return u.Commit(ctx)
}
