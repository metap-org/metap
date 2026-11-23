// Package action is how an application writes the custom logic behind an entity action (roadmap
// 120, P3): a function with typed input and output, a context that already knows who is asking,
// and the three things such logic needs besides its own rules:
//
//   - the records it reads and writes, through the caller's own permissions (Ctx.Records);
//   - an audit entry (Ctx.Audit);
//   - an event for the outbox (Ctx.Emit).
//
// Audit and events are optional: an action that calls neither writes neither. What it queues is
// written in one place, when a Step ends (or when the handler returns), so a failed handler leaves
// nothing behind and a handler never sees a transaction.
//
// An action is a value, declared once and registered with the service's ActionRegistry:
//
//	var VerifyDNS = action.Record(model.Zones, "verifyDns",
//		func(ctx context.Context, a *action.Ctx, zone Zone, _ action.None) (DNSResult, error) {
//			res := netcheck.CheckDNS(ctx, resolver, zone.Hostname) // the rules: plain Go, test as such
//			a.Audit("zone.dns_verified", a.ID, res)
//			a.Emit(events.Event{Topic: "waf.zone.dns_verified", AggregateType: model.Zones, AggregateID: a.ID, Payload: res})
//			return res, nil
//		})
//
// Who may run it is decided before the handler is reached (the route's entity-level check and the
// action's declared Requires); what the handler may read and write is bounded by the permissions
// behind Ctx.Records.
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

// Sink writes what a handler queued: outbox events inside a transaction of their own, audit entries
// after it commits (the audit store keeps its own hash chain). NewSink is the production one;
// actiontest has one that keeps everything in memory.
type Sink interface {
	// Emit enqueues the events for tenant in one transaction.
	Emit(ctx context.Context, tenantID uuid.UUID, evs []events.Event) error
	// Audit records the entries; a failure is the sink's to log, never the handler's to see.
	Audit(ctx context.Context, tenantID uuid.UUID, entries []audit.Entry)
}

// Deps is what every action of a service shares.
type Deps struct {
	// Records is what handlers read and write through. It should already enforce the caller's
	// permissions (a CrudService, or a backend wrapped with crud.Gated).
	Records crud.RecordBackend
	Sink    Sink
}

// Ctx is what a handler receives. It is not safe for concurrent use: a handler runs on one goroutine.
type Ctx struct {
	Entity string
	Name   string
	// ID is the record an action on one record acts on; uuid.Nil for an action on the entity.
	ID uuid.UUID
	RC *permission.RequestContext
	// Tenant is the caller's tenant.
	Tenant uuid.UUID
	// Records reads and writes entity records as the caller.
	Records crud.RecordBackend

	sink    Sink
	events  []events.Event
	entries []audit.Entry
}

// Emit queues an outbox event, written when the current Step ends (or the handler returns).
func (a *Ctx) Emit(e events.Event) { a.events = append(a.events, e) }

// Audit queues an audit entry for this action: name says what happened, recordID which record, and
// detail (anything JSON-marshalable) what is worth keeping. It is written when the current Step
// ends (or the handler returns), attributed to the caller.
func (a *Ctx) Audit(name string, recordID uuid.UUID, detail any) {
	diff := audit.JsonObject{}
	if detail != nil {
		if b, err := json.Marshal(detail); err == nil {
			diff["detail"] = b
		}
	}
	var actor *uuid.UUID
	if id, err := uuid.Parse(a.RC.UserID); err == nil {
		actor = &id
	}
	n := name
	a.entries = append(a.entries, audit.Entry{
		TenantID: a.Tenant, Entity: a.Entity, RecordID: recordID,
		Action: audit.ActionCustom, TransitionAction: &n,
		ActorUserID: actor, Diff: diff, OccurredAt: time.Now().UTC(),
	})
}

// Step runs one part of a multi-part action. When fn returns nil, what it queued is written; when it
// returns an error, what it queued is dropped and the error is returned. The handler itself runs
// outside any transaction, so a long action never holds a connection while it waits (roadmap 120,
// section 2.6); a step's own writes go through Records, each as the CRUD service commits it.
//
// name labels the step in errors and is what a later version will key resuming on.
func (a *Ctx) Step(ctx context.Context, name string, fn func(ctx context.Context) error) error {
	events, entries := len(a.events), len(a.entries)
	if err := fn(ctx); err != nil {
		a.events, a.entries = a.events[:events], a.entries[:entries]
		return fmt.Errorf("step %q: %w", name, err)
	}
	return a.flushFrom(ctx, events, entries)
}

// flushFrom writes everything queued from the given positions on and forgets it.
func (a *Ctx) flushFrom(ctx context.Context, fromEvents, fromEntries int) error {
	evs, entries := a.events[fromEvents:], a.entries[fromEntries:]
	a.events, a.entries = a.events[:fromEvents], a.entries[:fromEntries]
	if a.sink == nil {
		return nil
	}
	if len(evs) > 0 {
		if err := a.sink.Emit(ctx, a.Tenant, evs); err != nil {
			return err
		}
	}
	if len(entries) > 0 {
		a.sink.Audit(ctx, a.Tenant, entries)
	}
	return nil
}

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

// Entity declares an action on the entity as a whole: typed input in, typed output out.
func Entity[In, Out any](entity, name string, fn func(ctx context.Context, a *Ctx, in In) (Out, error)) Action {
	return Action{Entity: entity, Name: name, run: func(ctx context.Context, deps Deps, call crud.ActionCall) (crud.JsonObject, error) {
		in, err := decode[In](call.Input, true)
		if err != nil {
			return nil, err
		}
		a, err := newCtx(deps, call)
		if err != nil {
			return nil, err
		}
		return finish(ctx, a, func() (Out, error) { return fn(ctx, a, in) })
	}}
}

// Record declares an action on one record: the record is read first, as the caller, and decoded
// into Rec, so a record the caller may not read, or that does not exist, never reaches the handler.
func Record[Rec, In, Out any](entity, name string, fn func(ctx context.Context, a *Ctx, rec Rec, in In) (Out, error)) Action {
	return Action{Entity: entity, Name: name, run: func(ctx context.Context, deps Deps, call crud.ActionCall) (crud.JsonObject, error) {
		in, err := decode[In](call.Input, true)
		if err != nil {
			return nil, err
		}
		a, err := newCtx(deps, call)
		if err != nil {
			return nil, err
		}
		got, err := a.Records.Get(ctx, call.Entity, call.ID, call.RC)
		if err != nil {
			return nil, err
		}
		rec, err := decode[Rec](got.Record.Data, false)
		if err != nil {
			return nil, crud.NewServiceErrorMsg(http.StatusInternalServerError, errcode.InternalError, "the record does not fit the action's type: "+err.Error())
		}
		return finish(ctx, a, func() (Out, error) { return fn(ctx, a, rec, in) })
	}}
}

func newCtx(deps Deps, call crud.ActionCall) (*Ctx, error) {
	tenant, err := uuid.Parse(call.RC.TenantID)
	if err != nil {
		return nil, crud.NewServiceError(http.StatusUnauthorized, errcode.Unauthorized)
	}
	return &Ctx{
		Entity: call.Entity, Name: call.Name, ID: call.ID, RC: call.RC, Tenant: tenant,
		Records: deps.Records, sink: deps.Sink,
	}, nil
}

// finish runs the handler, writes what it left queued after its last Step, and encodes the answer.
// A handler that returns an error has nothing written.
func finish[Out any](ctx context.Context, a *Ctx, run func() (Out, error)) (crud.JsonObject, error) {
	out, err := run()
	if err != nil {
		return nil, err
	}
	if err := a.flushFrom(ctx, 0, 0); err != nil {
		return nil, err
	}
	return crud.ToObject(out, nil)
}

// decode reads a JSON object into T. For an action's input, strict refuses a field T does not have:
// a typo in a client's input is an error it sees, not a value that silently does nothing. A record
// is read leniently: it has fields the action has no use for.
func decode[T any](in crud.JsonObject, strict bool) (T, error) {
	var out T
	if len(in) == 0 {
		return out, nil
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return out, err
	}
	dec := json.NewDecoder(bytesReader(raw))
	if strict {
		dec.DisallowUnknownFields()
	}
	if err := dec.Decode(&out); err != nil {
		return out, crud.NewServiceErrorMsg(http.StatusBadRequest, errcode.ValidationFailed, "invalid input: "+err.Error())
	}
	return out, nil
}
