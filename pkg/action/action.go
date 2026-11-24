// Package action is how an application writes the custom logic behind an entity action (roadmap
// 120, P3) or any other operation that needs a unit of work: a context that already knows who is
// asking, and the three things such logic needs besides its own rules:
//
//   - the records it reads and writes, through the caller's own permissions (Ctx.Records);
//   - an audit entry (Ctx.Audit);
//   - an event for the outbox (Ctx.Emit, inside a Step only).
//
// There are two ways to write a handler, and the first is the default.
//
// Explicit: Custom gives the handler the Ctx and nothing else, and every step is a line in its body,
// so a handler that needs to differ (no record, an input that is optional, a step that must not
// happen) just does not call the step:
//
//	var VerifyDNS = action.Custom(model.Zones, "verifyDns",
//		func(ctx context.Context, a *action.Ctx) (any, error) {
//			var in verifyIn
//			if err := a.Decode(&in); err != nil { // strict: an unknown field is a 400
//				return nil, err
//			}
//			var zone Zone
//			if err := a.LoadRecord(ctx, &zone); err != nil { // as the caller; sets a.Current
//				return nil, err
//			}
//			res := netcheck.CheckDNS(ctx, resolver, zone.Hostname) // the rules: plain Go, test as such
//			err := a.Step(ctx, "mark", func(ctx context.Context) error {
//				if err := markVerified(ctx, a, res); err != nil {
//					return err
//				}
//				a.Audit("zone.dns_verified", a.ID, res)
//				return a.Emit(events.Event{Topic: "waf.zone.dns_verified", AggregateType: model.Zones, AggregateID: a.ID, Payload: res})
//			})
//			return res, err
//		})
//
// Shortcut: Record and Entity are those same steps in a fixed order (decode the input, read the
// record, call fn with both typed), for the handler that needs nothing else.
//
// Audit and events are optional: an action that calls neither writes neither. An event is written
// in the transaction of the Step that queued it, so it exists exactly when that Step's writes do;
// Emit outside a Step is an error (there is no transaction to put it in). What is queued is written
// when a Step ends (or, for audit, when the handler returns), so a failed handler leaves nothing
// behind and a handler never sees a transaction.
//
// Who may run it is decided before the handler is reached (the route's entity-level check and the
// action's declared Requires); what the handler may read and write is bounded by the permissions
// behind Ctx.Records.
//
// Unit is the part that does not depend on an entity (the caller, the tenant, Step, Audit, Emit):
// administrative operations use it directly.
package action

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/metap-org/metap/pkg/audit"
	"github.com/metap-org/metap/pkg/crud"
	"github.com/metap-org/metap/pkg/kit/errcode"
	"github.com/metap-org/metap/pkg/messaging/events"
	"github.com/metap-org/metap/pkg/permission"
)

// None is the input or output of an action that takes or returns nothing.
type None struct{}

// Sink is where an action's writes meet the database. Atomic runs one unit of work on the tenant's
// database; NewSink is the production one and actiontest has one that keeps everything in memory.
type Sink interface {
	// Atomic runs work in ONE transaction on tenant's database. Everything work does through the
	// context it is given (record writes, policy reads) joins that transaction, enqueue adds outbox
	// events to it, and a nil result commits all of it together while an error rolls all of it back.
	// Audit entries recorded by those writes wait for the commit.
	Atomic(ctx context.Context, tenantID uuid.UUID, work func(ctx context.Context, enqueue func([]events.Event) error) error) error
	// Audit records the entries once the unit has committed; a failure is the sink's to log, never
	// the handler's to see.
	Audit(ctx context.Context, tenantID uuid.UUID, entries []audit.Entry)
}

// Deps is what every action of a service shares.
type Deps struct {
	// Records is what handlers read and write through. It should already enforce the caller's
	// permissions (a CrudService, or a backend wrapped with crud.Gated).
	Records crud.RecordBackend
	Sink    Sink
}

// Unit is the part of a handler's context that does not depend on an entity: who is asking, the
// tenant, and Step, Audit and Emit. An entity action reaches it through Ctx; an administrative
// operation makes one with NewUnit and ends it with Close. It is not safe for concurrent use: a
// handler runs on one goroutine.
type Unit struct {
	RC *permission.RequestContext
	// Tenant is the caller's tenant.
	Tenant uuid.UUID

	sink    Sink
	events  []events.Event
	entries []audit.Entry
	inStep  bool
	// misuse is the first way the handler used the unit wrongly (Emit outside a Step). Close
	// reports it even if the handler ignored the error Emit returned.
	misuse error
}

// NewUnit starts a unit of work for the caller in rc. sink is nil in a handler that only reads
// (Step then just runs fn, and nothing is written).
func NewUnit(sink Sink, rc *permission.RequestContext) (*Unit, error) {
	if rc == nil {
		return nil, crud.NewServiceError(http.StatusUnauthorized, errcode.Unauthorized)
	}
	tenant, err := uuid.Parse(rc.TenantID)
	if err != nil {
		return nil, crud.NewServiceError(http.StatusUnauthorized, errcode.Unauthorized)
	}
	return &Unit{RC: rc, Tenant: tenant, sink: sink}, nil
}

// ErrEmitOutsideStep is what Emit returns when it is called outside a Step: an event belongs to
// the transaction of the writes it announces, and outside a Step there is none.
var ErrEmitOutsideStep = fmt.Errorf("action: Emit called outside a Step (an event is written in the transaction of the Step that queued it)")

// Emit queues an outbox event in the current Step, written in that Step's transaction. Called
// outside a Step it returns ErrEmitOutsideStep, and the action fails when it ends even if the
// handler ignores the error.
func (u *Unit) Emit(e events.Event) error {
	if !u.inStep {
		if u.misuse == nil {
			u.misuse = ErrEmitOutsideStep
		}
		return ErrEmitOutsideStep
	}
	u.events = append(u.events, e)
	return nil
}

// AuditEntry queues an audit entry for entity: name says what happened, recordID which record, and
// detail (anything JSON-marshalable) what is worth keeping. It is written when the current Step
// ends (or the unit is closed), attributed to the caller.
func (u *Unit) AuditEntry(entity, name string, recordID uuid.UUID, detail any) {
	diff := audit.JsonObject{}
	if detail != nil {
		if b, err := json.Marshal(detail); err == nil {
			diff["detail"] = b
		}
	}
	var actor *uuid.UUID
	if id, err := uuid.Parse(u.RC.UserID); err == nil {
		actor = &id
	}
	n := name
	u.entries = append(u.entries, audit.Entry{
		TenantID: u.Tenant, Entity: entity, RecordID: recordID,
		Action: audit.ActionCustom, TransitionAction: &n,
		ActorUserID: actor, Diff: diff, OccurredAt: time.Now().UTC(),
	})
}

// Step runs one part of a multi-part handler as one unit of work: the record writes it makes
// through Records, the events it queued with Emit, and the audit entries of both, commit together
// or not at all. When fn returns an error everything the step did is rolled back, what it queued
// is dropped, and the error is returned; earlier steps stay committed.
//
// Use the ctx fn is given: it carries the step's transaction. Keep fn to reads and writes of the
// tenant's records (no calls to other services, and nothing that takes a second connection from the
// pool): the step holds one database connection until it ends. The handler itself runs outside any
// transaction, so a long action never holds a connection while it waits (roadmap 120, section 2.6).
// Steps do not nest.
//
// name labels the step in errors.
func (u *Unit) Step(ctx context.Context, name string, fn func(ctx context.Context) error) error {
	if u.inStep {
		return fmt.Errorf("step %q: steps do not nest", name)
	}
	fromEvents, fromEntries := len(u.events), len(u.entries)
	u.inStep = true
	defer func() { u.inStep = false }()
	if u.sink == nil {
		err := fn(ctx)
		u.events = u.events[:fromEvents]
		if err != nil {
			u.entries = u.entries[:fromEntries]
			return fmt.Errorf("step %q: %w", name, err)
		}
		return nil
	}
	err := u.sink.Atomic(ctx, u.Tenant, func(ctx context.Context, enqueue func([]events.Event) error) error {
		if err := fn(ctx); err != nil {
			return err
		}
		return enqueue(u.events[fromEvents:])
	})
	if err != nil {
		u.events, u.entries = u.events[:fromEvents], u.entries[:fromEntries]
		return fmt.Errorf("step %q: %w", name, err)
	}
	u.recordAudit(ctx, fromEntries)
	u.events = u.events[:fromEvents]
	return nil
}

// recordAudit hands the entries queued from a position on to the sink and forgets them.
func (u *Unit) recordAudit(ctx context.Context, from int) {
	entries := u.entries[from:]
	u.entries = u.entries[:from]
	if u.sink != nil && len(entries) > 0 {
		u.sink.Audit(ctx, u.Tenant, entries)
	}
}

// Close ends the unit after the handler returned without error: it reports a misuse (an Emit
// outside a Step) and writes the audit entries queued after the last Step. A handler that returned
// an error must not call Close (it leaves nothing behind).
func (u *Unit) Close(ctx context.Context) error {
	if u.misuse != nil {
		return u.misuse
	}
	u.recordAudit(ctx, 0)
	return nil
}

// Ctx is what an entity action's handler receives: the Unit, plus the entity, the record and the
// input of the call.
type Ctx struct {
	*Unit

	Entity string
	Name   string
	// ID is the record an action on one record acts on; uuid.Nil for an action on the entity.
	ID uuid.UUID
	// Input is what the client sent, as the JSON object it was; Decode reads it into a type.
	Input crud.JsonObject
	// Records reads and writes entity records as the caller.
	Records crud.RecordBackend
	// Current is the record an action on one record was called for, as LoadRecord read it (its
	// Version is what an update of it expects). Zero until LoadRecord has run.
	Current crud.RecordDto
}

// Audit queues an audit entry for this action's entity: see Unit.AuditEntry.
func (a *Ctx) Audit(name string, recordID uuid.UUID, detail any) {
	a.AuditEntry(a.Entity, name, recordID, detail)
}

// Decode reads the call's input into out (a pointer to a struct). It is strict: a field out does
// not have is a 400, so a typo in a client's input is an error it sees and not a value that
// silently does nothing. An action with no input need not call it.
func (a *Ctx) Decode(out any) error {
	return decodeInto(a.Input, out, true)
}

// LoadRecord reads the record the action was called for, as the caller, sets Current, and decodes
// it into out (a pointer to a struct; nil to only set Current). A record the caller may not read,
// or that does not exist, is the error the caller would get from a plain read. The decode is
// lenient: a record has fields the action has no use for.
func (a *Ctx) LoadRecord(ctx context.Context, out any) error {
	if a.ID == uuid.Nil {
		return crud.NewServiceErrorMsg(http.StatusInternalServerError, errcode.InternalError, "LoadRecord called by an action on the entity, which has no record")
	}
	got, err := a.Records.Get(ctx, a.Entity, a.ID, a.RC)
	if err != nil {
		return err
	}
	a.Current = got.Record
	if out == nil {
		return nil
	}
	if err := decodeInto(got.Record.Data, out, false); err != nil {
		return crud.NewServiceErrorMsg(http.StatusInternalServerError, errcode.InternalError, "the record does not fit the action's type: "+err.Error())
	}
	return nil
}

// Handler is the explicit form of an action: it gets the Ctx and does each step itself. What it
// returns is encoded as the JSON object the client receives (nil is an empty object).
type Handler func(ctx context.Context, a *Ctx) (any, error)

// Action is a declared action: where it is registered and how it runs.
type Action struct {
	Entity string
	Name   string
	run    func(ctx context.Context, deps Deps, call crud.ActionCall) (crud.JsonObject, error)
}

// Register adds the action to reg, so POST /api/{entity}/.../actions/{name} reaches it.
func (x Action) Register(reg *crud.ActionRegistry, deps Deps) error {
	return reg.Register(x.Entity, x.Name, func(ctx context.Context, call crud.ActionCall) (crud.JsonObject, error) {
		return x.run(ctx, deps, call)
	})
}

// Run runs the action for call; Register's handler and actiontest both go through it.
func (x Action) Run(ctx context.Context, deps Deps, call crud.ActionCall) (crud.JsonObject, error) {
	return x.run(ctx, deps, call)
}

// Custom declares an action whose handler does each step itself (see the package comment). Nothing
// is read, decoded or opened for it: the handler calls Decode, LoadRecord and Step as it needs.
func Custom(entity, name string, fn Handler) Action {
	return Action{Entity: entity, Name: name, run: func(ctx context.Context, deps Deps, call crud.ActionCall) (crud.JsonObject, error) {
		u, err := NewUnit(deps.Sink, call.RC)
		if err != nil {
			return nil, err
		}
		a := &Ctx{Unit: u, Entity: call.Entity, Name: call.Name, ID: call.ID, Input: call.Input, Records: deps.Records}
		out, err := fn(ctx, a)
		if err != nil {
			return nil, err
		}
		if err := u.Close(ctx); err != nil {
			return nil, err
		}
		if out == nil {
			return crud.JsonObject{}, nil
		}
		return crud.ToObject(out, nil)
	}}
}

// Entity declares an action on the entity as a whole: typed input in, typed output out. It is
// Custom with the input decoded first.
func Entity[In, Out any](entity, name string, fn func(ctx context.Context, a *Ctx, in In) (Out, error)) Action {
	return Custom(entity, name, func(ctx context.Context, a *Ctx) (any, error) {
		var in In
		if err := a.Decode(&in); err != nil {
			return nil, err
		}
		return fn(ctx, a, in)
	})
}

// Record declares an action on one record: the input is decoded, then the record is read as the
// caller and decoded into Rec, so a record the caller may not read, or that does not exist, never
// reaches the handler. It is Custom with those two steps done first.
func Record[Rec, In, Out any](entity, name string, fn func(ctx context.Context, a *Ctx, rec Rec, in In) (Out, error)) Action {
	return Custom(entity, name, func(ctx context.Context, a *Ctx) (any, error) {
		var in In
		if err := a.Decode(&in); err != nil {
			return nil, err
		}
		var rec Rec
		if err := a.LoadRecord(ctx, &rec); err != nil {
			return nil, err
		}
		return fn(ctx, a, rec, in)
	})
}

// decodeInto reads a JSON object into out. Strict refuses a field out does not have.
func decodeInto(in crud.JsonObject, out any, strict bool) error {
	if len(in) == 0 {
		return nil
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytesReader(raw))
	if strict {
		dec.DisallowUnknownFields()
	}
	if err := dec.Decode(out); err != nil {
		return crud.NewServiceErrorMsg(http.StatusBadRequest, errcode.ValidationFailed, "invalid input: "+err.Error())
	}
	return nil
}
