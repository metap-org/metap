package kernel

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/metap-org/metap/pkg/action"
	"github.com/metap-org/metap/pkg/audit"
)

// CommitDespite wraps the error of a change that must still be committed: a wrong MFA code, whose
// attempt count has to survive the refusal or nothing would ever stop guessing. Change commits the
// work done so far (with no audit entry, as nothing changed) and returns Err.
type CommitDespite struct{ Err error }

func (c CommitDespite) Error() string { return c.Err.Error() }
func (c CommitDespite) Unwrap() error { return c.Err }

// Change runs one administrative change and records it. fn does the work (with the context it is given)
// and returns what to audit as a before/after pair per field.
//
// With a sink, the work and its audit entry are one transaction on the shared database: there is never
// a change without its entry or an entry for a change that was not made, whatever the tenant's
// strategy. Without one it is the older "change, then record best-effort" through fallback (nil
// records nothing). The entry is the one WriteAudit records, in the form the audit trail shows.
func Change(ctx context.Context, sink action.Sink, fallback Auditor, now func() time.Time, c Caller,
	entity string, id uuid.UUID, act audit.AuditAction, fn func(ctx context.Context) (map[string][2]any, error)) error {
	return ChangeNew(ctx, sink, fallback, now, c, entity, act, func(ctx context.Context) (uuid.UUID, map[string][2]any, error) {
		diff, err := fn(ctx)
		return id, diff, err
	})
}

// ChangeNew is Change for a change that creates the administered thing, so its id is known only once
// fn has run: fn returns it with what to audit.
func ChangeNew(ctx context.Context, sink action.Sink, fallback Auditor, now func() time.Time, c Caller,
	entity string, act audit.AuditAction, fn func(ctx context.Context) (uuid.UUID, map[string][2]any, error)) error {
	return run(ctx, sink, fallback, now, c, entity, act, true, fn)
}

// ChangeInTenant is Change for a change to the tenant's own data (its configuration, its roles): the
// work and the entry are one transaction on the tenant's database, where the work is, and the audit
// drain moves the entry from there into the one audit store. Work that opens a transaction through
// the tenant router joins it as a savepoint.
func ChangeInTenant(ctx context.Context, sink action.Sink, fallback Auditor, now func() time.Time, c Caller,
	entity string, id uuid.UUID, act audit.AuditAction, fn func(ctx context.Context) (map[string][2]any, error)) error {
	return run(ctx, sink, fallback, now, c, entity, act, false, func(ctx context.Context) (uuid.UUID, map[string][2]any, error) {
		diff, err := fn(ctx)
		return id, diff, err
	})
}

// ChangeInTenantNew is ChangeInTenant for a change that creates the thing it audits: fn returns its id.
func ChangeInTenantNew(ctx context.Context, sink action.Sink, fallback Auditor, now func() time.Time, c Caller,
	entity string, act audit.AuditAction, fn func(ctx context.Context) (uuid.UUID, map[string][2]any, error)) error {
	return run(ctx, sink, fallback, now, c, entity, act, false, fn)
}

func run(ctx context.Context, sink action.Sink, fallback Auditor, now func() time.Time, c Caller,
	entity string, act audit.AuditAction, shared bool, fn func(ctx context.Context) (uuid.UUID, map[string][2]any, error)) error {
	if now == nil {
		now = time.Now
	}
	if sink == nil {
		id, diff, err := fn(ctx)
		if err != nil {
			return err
		}
		if fallback != nil && diff != nil {
			fallback.Record(ctx, c, entity, id, act, diff)
		}
		return nil
	}
	newUnit := action.NewUnit
	if shared {
		newUnit = action.NewSharedUnit
	}
	unit, err := newUnit(sink, c.RC)
	if err != nil {
		return err
	}
	var refused error
	err = unit.Step(ctx, "change", func(ctx context.Context) error {
		id, diff, err := fn(ctx)
		if err != nil {
			if cd, ok := err.(CommitDespite); ok {
				refused = cd.Err
				return nil
			}
			return err
		}
		if diff != nil { // nil: the work made no change worth an entry
			unit.AuditPrepared(AuditEntryOf(c.Tenant, c.Self, entity, id, act, diff, now()))
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err := unit.Close(ctx); err != nil {
		return err
	}
	return refused
}
