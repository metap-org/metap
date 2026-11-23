// Package actiontest runs an action without a database, a broker or an HTTP server, and reports
// what it did: the answer, the records it wrote, the events it queued and the audit entries it
// recorded. An action's rules are plain functions and are tested as such; this is for the thin
// part that decides what to read, write, audit and emit.
package actiontest

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"

	"github.com/google/uuid"

	"github.com/metap-org/metap/pkg/action"
	"github.com/metap-org/metap/pkg/audit"
	"github.com/metap-org/metap/pkg/crud"
	"github.com/metap-org/metap/pkg/kit/errcode"
	"github.com/metap-org/metap/pkg/messaging/events"
	"github.com/metap-org/metap/pkg/permission"
)

// Sink keeps in memory what an action emitted and audited. Set EmitErr to make Emit fail, to see
// how an action behaves when the outbox does.
type Sink struct {
	mu      sync.Mutex
	Events  []events.Event
	Entries []audit.Entry
	EmitErr error
}

// Atomic runs work once and keeps the events it enqueued only if it succeeds, the way the database
// sink commits or rolls back. There is no transaction here: the record writes an action makes
// through Records are kept in Records.Writes either way, so a test that needs "a failed step undoes
// its writes" belongs in the integration suite.
func (s *Sink) Atomic(ctx context.Context, _ uuid.UUID, work func(ctx context.Context, enqueue func([]events.Event) error) error) error {
	var pending []events.Event
	err := work(ctx, func(evs []events.Event) error {
		if s.EmitErr != nil {
			return s.EmitErr
		}
		pending = append(pending, evs...)
		return nil
	})
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Events = append(s.Events, pending...)
	return nil
}

func (s *Sink) Audit(_ context.Context, _ uuid.UUID, entries []audit.Entry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Entries = append(s.Entries, entries...)
}

// Write is one record write an action made.
type Write struct {
	Op     string // "create", "update", "transition", "delete"
	Entity string
	ID     uuid.UUID
	Action string // the workflow action, for a transition
	Data   crud.JsonObject
}

// Records is an in-memory crud.RecordBackend: it serves the records put into Stored and keeps the
// writes an action makes. A read of a record that is not stored is a 404, like the real service.
// List, GetMany and Aggregate are not implemented; an action that calls them needs a fuller fake.
type Records struct {
	crud.RecordBackend
	mu     sync.Mutex
	Stored map[uuid.UUID]crud.RecordDto
	Writes []Write
	// WriteErr, when set, is returned by every write.
	WriteErr error
}

func (r *Records) Get(_ context.Context, entity string, id uuid.UUID, _ *permission.RequestContext) (crud.GetResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.Stored[id]
	if !ok || rec.Entity != entity {
		return crud.GetResult{}, crud.NewServiceError(http.StatusNotFound, errcode.RecordNotFound)
	}
	return crud.GetResult{Record: rec}, nil
}

func (r *Records) write(w Write) (crud.RecordDto, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.WriteErr != nil {
		return crud.RecordDto{}, r.WriteErr
	}
	r.Writes = append(r.Writes, w)
	return crud.RecordDto{ID: w.ID, Entity: w.Entity, Data: w.Data, Version: 1}, nil
}

func (r *Records) Create(_ context.Context, entity string, data crud.JsonObject, _ *permission.RequestContext, _ *string) (crud.RecordDto, error) {
	return r.write(Write{Op: "create", Entity: entity, ID: uuid.New(), Data: data})
}

func (r *Records) Update(_ context.Context, entity string, id uuid.UUID, _ int32, data crud.JsonObject, _ *permission.RequestContext, _ *string) (crud.RecordDto, error) {
	return r.write(Write{Op: "update", Entity: entity, ID: id, Data: data})
}

func (r *Records) Transition(_ context.Context, entity string, id uuid.UUID, act string, _ int32, data crud.JsonObject, _ *permission.RequestContext, _ *string) (crud.RecordDto, error) {
	return r.write(Write{Op: "transition", Entity: entity, ID: id, Action: act, Data: data})
}

func (r *Records) Delete(_ context.Context, entity string, id uuid.UUID, _ int32, _ *permission.RequestContext, _ *string) (crud.RecordDto, error) {
	return r.write(Write{Op: "delete", Entity: entity, ID: id})
}

// Store puts a record where Get will find it.
func (r *Records) Store(entity string, id uuid.UUID, data map[string]any) {
	obj := crud.JsonObject{}
	for k, v := range data {
		b, _ := json.Marshal(v)
		obj[k] = b
	}
	if r.Stored == nil {
		r.Stored = map[uuid.UUID]crud.RecordDto{}
	}
	r.Stored[id] = crud.RecordDto{ID: id, Entity: entity, Data: obj, Version: 1}
}

// Caller is a request context for a member of a fresh tenant holding roles.
func Caller(roles ...string) *permission.RequestContext {
	return &permission.RequestContext{TenantID: uuid.NewString(), UserID: uuid.NewString(), Roles: roles, HasRoles: true}
}

// Call builds the invocation of an action: id is the record for an action on one record (uuid.Nil
// otherwise) and input the JSON object the client sent.
func Call(entity, name string, id uuid.UUID, input map[string]any, rc *permission.RequestContext) crud.ActionCall {
	obj := crud.JsonObject{}
	for k, v := range input {
		b, _ := json.Marshal(v)
		obj[k] = b
	}
	return crud.ActionCall{Entity: entity, Name: name, ID: id, Input: obj, RC: rc}
}

// Result is what running an action did.
type Result struct {
	Out     crud.JsonObject
	Err     error
	Sink    *Sink
	Records *Records
}

// Run runs x for call with in-memory records and sink. Seed the records first with seed (nil for
// none): seed(r) calls r.Store for each record the action should find.
func Run(x action.Action, call crud.ActionCall, seed func(*Records)) Result {
	records, sink := &Records{}, &Sink{}
	if seed != nil {
		seed(records)
	}
	return RunWith(x, call, records, sink)
}

// RunWith is Run with the records and sink the test built itself, to set WriteErr or EmitErr.
func RunWith(x action.Action, call crud.ActionCall, records *Records, sink *Sink) Result {
	out, err := x.Run(context.Background(), action.Deps{Records: records, Sink: sink}, call)
	return Result{Out: out, Err: err, Sink: sink, Records: records}
}
