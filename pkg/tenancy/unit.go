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
	tx, err := r.begin(ctx, tenantID)
	if err != nil {
		return ctx, nil, err
	}
	u := &Unit{tenant: tenantID, tx: tx}
	return context.WithValue(ctx, unitKey{}, u), u, nil
}

func unitFrom(ctx context.Context) *Unit {
	u, _ := ctx.Value(unitKey{}).(*Unit)
	return u
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
