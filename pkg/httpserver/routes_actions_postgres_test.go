//go:build integration

package httpserver_test

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/metap-org/metap/pkg/auth/jwks"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/action"
	"github.com/metap-org/metap/pkg/audit"
	"github.com/metap-org/metap/pkg/crud"
	"github.com/metap-org/metap/pkg/messaging/events"
	"github.com/metap-org/metap/pkg/metadata"
)

// Declared actions over REST (roadmap 116): record- and entity-scoped, only what the entity
// declares, the handler's ServiceError in the standard envelope.
func TestDeclaredActionsOverREST(t *testing.T) {
	pool := httpTestPool(t)
	ctx := context.Background()
	tenantID, userID := uuid.New(), uuid.New()
	_, err := pool.Exec(ctx, "INSERT INTO metadata.user_roles (tenant_id, user_id, role) VALUES ($1, $2, 'admin')", tenantID, userID)
	require.NoError(t, err)
	t.Cleanup(func() {
		pool.Exec(context.Background(), "DELETE FROM metadata.user_roles WHERE tenant_id = $1", tenantID)
	})

	entity := httpTestEntity()
	entity.Actions = []metadata.EntityAction{
		{Name: "ping", Scope: metadata.ActionScopeRecord},
		{Name: "sweep", Scope: metadata.ActionScopeEntity},
	}
	state, privPEM := newTestAppState(t, pool, entity)
	reg := crud.NewActionRegistry()
	require.NoError(t, reg.Register("test.orders", "ping", func(_ context.Context, call crud.ActionCall) (crud.JsonObject, error) {
		if string(call.Input["fail"]) == "true" {
			return nil, crud.NewServiceErrorMsg(http.StatusConflict, "not_ready", "Not ready yet.")
		}
		id, _ := json.Marshal(call.ID.String())
		return crud.JsonObject{"pinged": id}, nil
	}))
	require.NoError(t, reg.Register("test.orders", "sweep", func(_ context.Context, call crud.ActionCall) (crud.JsonObject, error) {
		return crud.JsonObject{"swept": json.RawMessage("3")}, nil
	}))
	state.Actions = reg
	token, err := jwks.MintRSAJWT(privPEM, tenantID, userID, 3600)
	require.NoError(t, err)
	base := startTestServer(t, state, nil)

	id := uuid.NewString()
	res := doRequest(t, "POST", base+"/api/test.orders/"+id+"/actions/ping", token, nil)
	require.Equal(t, 200, res.StatusCode)
	assert.Equal(t, id, decodeJSON(t, res)["data"].(map[string]any)["pinged"])

	res = doRequest(t, "POST", base+"/api/test.orders/actions/sweep", token, map[string]any{"input": map[string]any{}})
	require.Equal(t, 200, res.StatusCode)
	assert.EqualValues(t, 3, decodeJSON(t, res)["data"].(map[string]any)["swept"])

	res = doRequest(t, "POST", base+"/api/test.orders/"+id+"/actions/ping", token, map[string]any{"input": map[string]any{"fail": true}})
	require.Equal(t, 409, res.StatusCode)
	assert.Equal(t, "not_ready", decodeJSON(t, res)["error"].(map[string]any)["code"])

	for _, path := range []string{"/api/test.orders/actions/ping", "/api/test.orders/" + id + "/actions/sweep", "/api/test.orders/actions/nope"} {
		res = doRequest(t, "POST", base+path, token, nil)
		assert.Equal(t, 404, res.StatusCode, fmt.Sprintf("%s: wrong scope or undeclared", path))
		assert.Equal(t, "action_not_found", decodeJSON(t, res)["error"].(map[string]any)["code"])
	}
	assert.Equal(t, 401, doRequest(t, "POST", base+"/api/test.orders/actions/sweep", "", nil).StatusCode)
}

// An action that declares Requires is refused before its handler runs unless the caller holds that
// permission on the entity — the handler of an action that touches no record has no other gate.
func TestDeclaredActionRequiresEntityPermission(t *testing.T) {
	pool := httpTestPool(t)
	ctx := context.Background()
	tenantID, adminID, viewerID := uuid.New(), uuid.New(), uuid.New()
	for user, role := range map[uuid.UUID]string{adminID: "admin", viewerID: "viewer"} {
		_, err := pool.Exec(ctx, "INSERT INTO metadata.user_roles (tenant_id, user_id, role) VALUES ($1, $2, $3)", tenantID, user, role)
		require.NoError(t, err)
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), "DELETE FROM metadata.user_roles WHERE tenant_id = $1", tenantID)
	})

	entity := httpTestEntity()
	entity.Actions = []metadata.EntityAction{{Name: "probe", Scope: metadata.ActionScopeEntity, Requires: "create"}}
	state, privPEM := newTestAppState(t, pool, entity)
	calls := 0
	reg := crud.NewActionRegistry()
	require.NoError(t, reg.Register("test.orders", "probe", func(context.Context, crud.ActionCall) (crud.JsonObject, error) {
		calls++
		return crud.JsonObject{"ok": json.RawMessage("true")}, nil
	}))
	state.Actions = reg
	base := startTestServer(t, state, nil)

	viewer, err := jwks.MintRSAJWT(privPEM, tenantID, viewerID, 3600)
	require.NoError(t, err)
	res := doRequest(t, "POST", base+"/api/test.orders/actions/probe", viewer, nil)
	assert.Equal(t, 403, res.StatusCode)
	assert.Zero(t, calls, "the handler must not run for a caller without the permission")

	admin, err := jwks.MintRSAJWT(privPEM, tenantID, adminID, 3600)
	require.NoError(t, err)
	res = doRequest(t, "POST", base+"/api/test.orders/actions/probe", admin, nil)
	assert.Equal(t, 200, res.StatusCode)
	assert.Equal(t, 1, calls)
}

// A typed action (pkg/action) registered with the state's own dependencies: it reads its record as
// the caller, writes through the same backend the routes use, and what it audits and emits is
// written, over a real route and a real database.
func TestTypedActionOverRESTWritesRecordAuditAndOutboxEvent(t *testing.T) {
	pool := httpTestPool(t)
	ctx := context.Background()
	tenantID, userID := uuid.New(), uuid.New()
	_, err := pool.Exec(ctx, "INSERT INTO metadata.user_roles (tenant_id, user_id, role) VALUES ($1, $2, 'admin')", tenantID, userID)
	require.NoError(t, err)
	t.Cleanup(func() {
		pool.Exec(context.Background(), "DELETE FROM metadata.user_roles WHERE tenant_id = $1", tenantID)
	})

	ensureTestOrdersTable(t, pool)
	entity := httpTestEntity()
	entity.Actions = []metadata.EntityAction{{Name: "rename", Scope: metadata.ActionScopeRecord, Requires: "update"}}
	state, privPEM := newTestAppState(t, pool, entity)
	auditStore := audit.NewPostgresStore(pool)
	state.Audit = auditStore

	type order struct {
		Name string `json:"name"`
	}
	type renameIn struct {
		Name string `json:"name"`
	}
	type renameOut struct {
		Was string `json:"was"`
		Now string `json:"now"`
	}
	rename := action.Record("test.orders", "rename",
		func(ctx context.Context, a *action.Ctx, o order, in renameIn) (renameOut, error) {
			name, _ := json.Marshal(in.Name)
			if _, err := a.Records.Update(ctx, a.Entity, a.ID, 1, crud.JsonObject{"name": name}, a.RC, nil); err != nil {
				return renameOut{}, err
			}
			out := renameOut{Was: o.Name, Now: in.Name}
			a.Audit("order.renamed", a.ID, out)
			a.Emit(events.Event{Topic: "test.order.renamed", AggregateType: a.Entity, AggregateID: a.ID, Payload: out})
			return out, nil
		})
	reg := crud.NewActionRegistry()
	require.NoError(t, rename.Register(reg, state.ActionDeps()))
	state.Actions = reg

	token, err := jwks.MintRSAJWT(privPEM, tenantID, userID, 3600)
	require.NoError(t, err)
	base := startTestServer(t, state, nil)

	res := doRequest(t, "POST", base+"/api/test.orders", token, map[string]any{"data": map[string]any{"name": "first"}})
	require.Equal(t, 201, res.StatusCode)
	id := decodeJSON(t, res)["data"].(map[string]any)["id"].(string)

	res = doRequest(t, "POST", base+"/api/test.orders/"+id+"/actions/rename", token, map[string]any{"input": map[string]any{"name": "second"}})
	require.Equal(t, 200, res.StatusCode)
	out := decodeJSON(t, res)["data"].(map[string]any)
	assert.Equal(t, "first", out["was"])
	assert.Equal(t, "second", out["now"])

	res = doRequest(t, "GET", base+"/api/test.orders/"+id, token, nil)
	require.Equal(t, 200, res.StatusCode)
	assert.Equal(t, "second", decodeJSON(t, res)["data"].(map[string]any)["data"].(map[string]any)["name"])

	recordID := uuid.MustParse(id)
	var events int
	require.NoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM metadata.outbox_events WHERE topic = 'test.order.renamed' AND aggregate_id = $1", recordID).Scan(&events))
	assert.Equal(t, 1, events, "the event the action emitted is in the outbox")

	rows, err := auditStore.Query(ctx, tenantID, audit.Query{Entity: "test.orders", RecordID: recordID})
	require.NoError(t, err)
	var named []string
	for _, r := range rows {
		if r.Action == "action" && r.TransitionAction != nil {
			named = append(named, *r.TransitionAction)
		}
	}
	assert.Equal(t, []string{"order.renamed"}, named, "the action's audit entry is in the trail")

	// A field the action does not declare is refused with the standard 400, and nothing is written.
	res = doRequest(t, "POST", base+"/api/test.orders/"+id+"/actions/rename", token, map[string]any{"input": map[string]any{"nme": "x"}})
	require.Equal(t, 400, res.StatusCode)
	assert.Equal(t, "validation_failed", decodeJSON(t, res)["error"].(map[string]any)["code"])
}

// A Step is one unit of work: the record write, the event and the audit entries of a step commit
// together. When the step fails after the record was written, the record is as it was, nothing is
// in the outbox, and nothing is in the audit trail (neither the action's nor the record write's).
func TestAStepThatFailsUndoesItsRecordWriteAndLeavesNoEventOrAudit(t *testing.T) {
	pool := httpTestPool(t)
	ctx := context.Background()
	tenantID, userID := uuid.New(), uuid.New()
	_, err := pool.Exec(ctx, "INSERT INTO metadata.user_roles (tenant_id, user_id, role) VALUES ($1, $2, 'admin')", tenantID, userID)
	require.NoError(t, err)
	t.Cleanup(func() {
		pool.Exec(context.Background(), "DELETE FROM metadata.user_roles WHERE tenant_id = $1", tenantID)
	})

	ensureTestOrdersTable(t, pool)
	entity := httpTestEntity()
	entity.Audit = &metadata.EntityAuditConfig{Enabled: true}
	entity.Actions = []metadata.EntityAction{
		{Name: "renameTwice", Scope: metadata.ActionScopeRecord},
		{Name: "renameOnce", Scope: metadata.ActionScopeRecord},
	}
	state, privPEM := newTestAppState(t, pool, entity)
	auditStore := audit.NewPostgresStore(pool)
	state.Audit = auditStore
	state.Crud = crud.WithAudit(state.Router, state.Metadata, state.Permissions, auditStore)

	type none = action.None
	rename := func(ctx context.Context, a *action.Ctx, name string) error {
		raw, _ := json.Marshal(name)
		_, err := a.Records.Update(ctx, a.Entity, a.ID, 1, crud.JsonObject{"name": raw}, a.RC, nil)
		return err
	}
	// Writes the record, queues a good event, then queues an event the outbox refuses.
	renameTwice := action.Record("test.orders", "renameTwice",
		func(ctx context.Context, a *action.Ctx, _ struct{}, _ none) (none, error) {
			return none{}, a.Step(ctx, "rename", func(ctx context.Context) error {
				if err := rename(ctx, a, "changed"); err != nil {
					return err
				}
				a.Audit("order.renamed", a.ID, nil)
				a.Emit(events.Event{Topic: "test.order.renamed", AggregateType: a.Entity, AggregateID: a.ID})
				a.Emit(events.Event{Topic: "NOT A TOPIC", AggregateType: a.Entity, AggregateID: a.ID})
				return nil
			})
		})
	var heldInStep int32
	renameOnce := action.Record("test.orders", "renameOnce",
		func(ctx context.Context, a *action.Ctx, _ struct{}, _ none) (none, error) {
			return none{}, a.Step(ctx, "rename", func(ctx context.Context) error {
				if err := rename(ctx, a, "kept"); err != nil {
					return err
				}
				// The record write, its policy reads and the audit bookkeeping all ran on the step's
				// one connection: no second one is held while the step is open.
				heldInStep = pool.Stat().AcquiredConns()
				a.Audit("order.renamed", a.ID, nil)
				a.Emit(events.Event{Topic: "test.order.renamed", AggregateType: a.Entity, AggregateID: a.ID})
				return nil
			})
		})
	reg := crud.NewActionRegistry()
	deps := state.ActionDeps()
	require.NoError(t, renameTwice.Register(reg, deps))
	require.NoError(t, renameOnce.Register(reg, deps))
	state.Actions = reg

	token, err := jwks.MintRSAJWT(privPEM, tenantID, userID, 3600)
	require.NoError(t, err)
	base := startTestServer(t, state, nil)

	res := doRequest(t, "POST", base+"/api/test.orders", token, map[string]any{"data": map[string]any{"name": "first"}})
	require.Equal(t, 201, res.StatusCode)
	id := decodeJSON(t, res)["data"].(map[string]any)["id"].(string)
	recordID := uuid.MustParse(id)
	nameOf := func() string {
		res := doRequest(t, "GET", base+"/api/test.orders/"+id, token, nil)
		require.Equal(t, 200, res.StatusCode)
		return decodeJSON(t, res)["data"].(map[string]any)["data"].(map[string]any)["name"].(string)
	}
	outbox := func() int {
		var n int
		require.NoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM metadata.outbox_events WHERE topic = 'test.order.renamed' AND aggregate_id = $1", recordID).Scan(&n))
		return n
	}
	updates := func() int {
		rows, err := auditStore.Query(ctx, tenantID, audit.Query{Entity: "test.orders", RecordID: recordID})
		require.NoError(t, err)
		n := 0
		for _, r := range rows {
			if r.Action == "update" || r.Action == "action" {
				n++
			}
		}
		return n
	}

	res = doRequest(t, "POST", base+"/api/test.orders/"+id+"/actions/renameTwice", token, nil)
	require.GreaterOrEqual(t, res.StatusCode, 400, "the step failed, so the action did")
	assert.Equal(t, "first", nameOf(), "the record write rolled back with the step")
	assert.Equal(t, 0, outbox(), "no event of the failed step is in the outbox")
	assert.Equal(t, 0, updates(), "no audit entry for a write that did not stay: neither the record's update nor the action's")

	res = doRequest(t, "POST", base+"/api/test.orders/"+id+"/actions/renameOnce", token, nil)
	require.Equal(t, 200, res.StatusCode)
	assert.Equal(t, "kept", nameOf(), "a step that succeeds keeps its write...")
	assert.Equal(t, 1, outbox(), "...its event...")
	assert.Equal(t, 2, updates(), "...and both audit entries: the record's update and the action's")
	assert.EqualValues(t, 1, heldInStep, "a step holds one database connection, however many reads and writes it makes")
}
