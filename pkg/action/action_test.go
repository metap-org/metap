package action_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/action"
	"github.com/metap-org/metap/pkg/action/actiontest"
	"github.com/metap-org/metap/pkg/audit"
	"github.com/metap-org/metap/pkg/crud"
	"github.com/metap-org/metap/pkg/messaging/events"
)

// The actions below are examples of the shape an application writes. Their rules (checkHost) are
// plain functions, tested on their own; the action only decides what to read, write, audit, emit.

type zone struct {
	Hostname string `json:"hostname"`
	Status   string `json:"status"`
}

type verifyIn struct {
	Method string `json:"method"`
}

type verifyOut struct {
	Verified bool   `json:"verified"`
	Host     string `json:"host"`
}

func checkHost(host, method string) bool { return host != "" && method == "txt" }

var verifyDNS = action.Record("waf.zones", "verifyDns",
	func(ctx context.Context, a *action.Ctx, z zone, in verifyIn) (verifyOut, error) {
		out := verifyOut{Verified: checkHost(z.Hostname, in.Method), Host: z.Hostname}
		a.Audit("zone.dns_checked", a.ID, out)
		if out.Verified {
			if _, err := a.Records.Update(ctx, a.Entity, a.ID, 1, crud.JsonObject{"status": []byte(`"active"`)}, a.RC, nil); err != nil {
				return verifyOut{}, err
			}
			a.Emit(events.Event{Topic: "waf.zone.verified", AggregateType: a.Entity, AggregateID: a.ID, Payload: out})
		}
		return out, nil
	})

func seeded(id uuid.UUID) func(*actiontest.Records) {
	return func(r *actiontest.Records) {
		r.Store("waf.zones", id, map[string]any{"hostname": "example.com", "status": "pending"})
	}
}

func TestARecordActionGetsItsRecordAndTypedInputAndAnswersWithTypedOutput(t *testing.T) {
	id := uuid.New()
	call := actiontest.Call("waf.zones", "verifyDns", id, map[string]any{"method": "txt"}, actiontest.Caller("analyst"))
	res := actiontest.Run(verifyDNS, call, seeded(id))
	require.NoError(t, res.Err)
	require.JSONEq(t, `true`, string(res.Out["verified"]))
	require.JSONEq(t, `"example.com"`, string(res.Out["host"]))
}

func TestWhatTheHandlerWritesAuditsAndEmitsIsReported(t *testing.T) {
	id := uuid.New()
	rc := actiontest.Caller("analyst")
	res := actiontest.Run(verifyDNS, actiontest.Call("waf.zones", "verifyDns", id, map[string]any{"method": "txt"}, rc), seeded(id))
	require.NoError(t, res.Err)

	require.Len(t, res.Records.Writes, 1)
	require.Equal(t, "update", res.Records.Writes[0].Op)
	require.Equal(t, id, res.Records.Writes[0].ID)

	require.Len(t, res.Sink.Events, 1)
	require.Equal(t, "waf.zone.verified", res.Sink.Events[0].Topic)

	require.Len(t, res.Sink.Entries, 1)
	e := res.Sink.Entries[0]
	require.Equal(t, audit.ActionCustom, e.Action)
	require.Equal(t, "zone.dns_checked", *e.TransitionAction)
	require.Equal(t, "waf.zones", e.Entity)
	require.Equal(t, id, e.RecordID)
	require.Equal(t, rc.UserID, e.ActorUserID.String(), "the entry is attributed to the caller")
	require.Contains(t, e.Diff, "detail")
}

func TestAnActionThatDoesNotAuditOrEmitWritesNeither(t *testing.T) {
	id := uuid.New()
	quiet := action.Record("waf.zones", "peek", func(_ context.Context, _ *action.Ctx, z zone, _ action.None) (zone, error) {
		return z, nil
	})
	res := actiontest.Run(quiet, actiontest.Call("waf.zones", "peek", id, nil, actiontest.Caller()), seeded(id))
	require.NoError(t, res.Err)
	require.Empty(t, res.Sink.Events)
	require.Empty(t, res.Sink.Entries)
	require.Empty(t, res.Records.Writes)
}

func TestARecordTheCallerCannotGetNeverReachesTheHandler(t *testing.T) {
	reached := false
	a := action.Record("waf.zones", "peek", func(_ context.Context, _ *action.Ctx, z zone, _ action.None) (zone, error) {
		reached = true
		return z, nil
	})
	res := actiontest.Run(a, actiontest.Call("waf.zones", "peek", uuid.New(), nil, actiontest.Caller()), nil)
	se, ok := crud.AsServiceError(res.Err)
	require.True(t, ok)
	require.Equal(t, http.StatusNotFound, int(se.Status))
	require.False(t, reached)
}

func TestAnInputFieldTheActionDoesNotHaveIsRefused(t *testing.T) {
	id := uuid.New()
	res := actiontest.Run(verifyDNS, actiontest.Call("waf.zones", "verifyDns", id, map[string]any{"methd": "txt"}, actiontest.Caller()), seeded(id))
	se, ok := crud.AsServiceError(res.Err)
	require.True(t, ok)
	require.Equal(t, http.StatusBadRequest, int(se.Status))
	require.Equal(t, "validation_failed", se.Code)
	require.Empty(t, res.Records.Writes)
}

func TestAHandlerThatFailsLeavesNothingQueuedBehind(t *testing.T) {
	boom := errors.New("boom")
	a := action.Entity("waf.zones", "explode", func(_ context.Context, a *action.Ctx, _ action.None) (action.None, error) {
		a.Audit("zone.exploded", uuid.New(), nil)
		a.Emit(events.Event{Topic: "waf.zone.exploded", AggregateType: "waf.zones", AggregateID: uuid.New()})
		return action.None{}, boom
	})
	res := actiontest.Run(a, actiontest.Call("waf.zones", "explode", uuid.Nil, nil, actiontest.Caller()), nil)
	require.ErrorIs(t, res.Err, boom)
	require.Empty(t, res.Sink.Events, "an error in the handler means nothing it queued is written")
	require.Empty(t, res.Sink.Entries)
}

func TestEachStepWritesWhatItQueuedAndAFailedStepDropsOnlyItsOwn(t *testing.T) {
	boom := errors.New("second step failed")
	a := action.Entity("waf.zones", "apply", func(ctx context.Context, a *action.Ctx, _ action.None) (action.None, error) {
		if err := a.Step(ctx, "first", func(context.Context) error {
			a.Audit("step.first", uuid.New(), nil)
			a.Emit(events.Event{Topic: "waf.step.first", AggregateType: "waf.zones", AggregateID: uuid.New()})
			return nil
		}); err != nil {
			return action.None{}, err
		}
		err := a.Step(ctx, "second", func(context.Context) error {
			a.Audit("step.second", uuid.New(), nil)
			a.Emit(events.Event{Topic: "waf.step.second", AggregateType: "waf.zones", AggregateID: uuid.New()})
			return boom
		})
		return action.None{}, err
	})
	res := actiontest.Run(a, actiontest.Call("waf.zones", "apply", uuid.Nil, nil, actiontest.Caller()), nil)

	require.ErrorIs(t, res.Err, boom)
	require.Contains(t, res.Err.Error(), `step "second"`)
	require.Len(t, res.Sink.Events, 1, "the first step committed before the second failed")
	require.Equal(t, "waf.step.first", res.Sink.Events[0].Topic)
	require.Len(t, res.Sink.Entries, 1)
	require.Equal(t, "step.first", *res.Sink.Entries[0].TransitionAction)
}

func TestAStepErrorKeepsTheServiceErrorStatus(t *testing.T) {
	a := action.Entity("waf.zones", "apply", func(ctx context.Context, a *action.Ctx, _ action.None) (action.None, error) {
		return action.None{}, a.Step(ctx, "write", func(context.Context) error {
			return crud.NewServiceError(http.StatusConflict, "version_conflict")
		})
	})
	res := actiontest.Run(a, actiontest.Call("waf.zones", "apply", uuid.Nil, nil, actiontest.Caller()), nil)
	se, ok := crud.AsServiceError(res.Err)
	require.True(t, ok, "wrapping the step name must not hide the status")
	require.Equal(t, http.StatusConflict, int(se.Status))
}

func TestWhatIsQueuedAfterTheLastStepIsWrittenWhenTheHandlerReturns(t *testing.T) {
	a := action.Entity("waf.zones", "apply", func(ctx context.Context, a *action.Ctx, _ action.None) (action.None, error) {
		if err := a.Step(ctx, "only", func(context.Context) error { a.Audit("in.step", uuid.New(), nil); return nil }); err != nil {
			return action.None{}, err
		}
		a.Audit("after.step", uuid.New(), nil)
		return action.None{}, nil
	})
	res := actiontest.Run(a, actiontest.Call("waf.zones", "apply", uuid.Nil, nil, actiontest.Caller()), nil)
	require.NoError(t, res.Err)
	require.Len(t, res.Sink.Entries, 2)
	require.Equal(t, "in.step", *res.Sink.Entries[0].TransitionAction)
	require.Equal(t, "after.step", *res.Sink.Entries[1].TransitionAction)
}

func TestAnOutboxFailureIsTheActionsFailure(t *testing.T) {
	boom := errors.New("outbox down")
	a := action.Entity("waf.zones", "announce", func(_ context.Context, a *action.Ctx, _ action.None) (action.None, error) {
		a.Emit(events.Event{Topic: "waf.zone.announced", AggregateType: "waf.zones", AggregateID: uuid.New()})
		return action.None{}, nil
	})
	sink := &actiontest.Sink{EmitErr: boom}
	res := actiontest.RunWith(a, actiontest.Call("waf.zones", "announce", uuid.Nil, nil, actiontest.Caller()), &actiontest.Records{}, sink)
	require.ErrorIs(t, res.Err, boom)
}

func TestARegisteredActionRunsThroughTheRegistry(t *testing.T) {
	id := uuid.New()
	records, sink := &actiontest.Records{}, &actiontest.Sink{}
	seeded(id)(records)

	reg := crud.NewActionRegistry()
	require.NoError(t, verifyDNS.Register(reg, action.Deps{Records: records, Sink: sink}))
	require.True(t, reg.Has("waf.zones", "verifyDns"))

	out, err := reg.Invoke(context.Background(), actiontest.Call("waf.zones", "verifyDns", id, map[string]any{"method": "txt"}, actiontest.Caller()))
	require.NoError(t, err)
	require.JSONEq(t, `true`, string(out["verified"]))
	require.Len(t, sink.Events, 1)
}

func TestAnActionRegisteredTwiceIsAnError(t *testing.T) {
	reg := crud.NewActionRegistry()
	require.NoError(t, verifyDNS.Register(reg, action.Deps{}))
	require.Error(t, verifyDNS.Register(reg, action.Deps{}))
}

func TestARecordWithFieldsTheActionDoesNotDeclareStillReachesIt(t *testing.T) {
	id := uuid.New()
	type onlyHost struct {
		Hostname string `json:"hostname"`
	}
	a := action.Record("waf.zones", "host", func(_ context.Context, _ *action.Ctx, z onlyHost, _ action.None) (onlyHost, error) {
		return z, nil
	})
	// The stored record also has "status"; the action asks for hostname only.
	res := actiontest.Run(a, actiontest.Call("waf.zones", "host", id, nil, actiontest.Caller()), seeded(id))
	require.NoError(t, res.Err)
	require.JSONEq(t, `"example.com"`, string(res.Out["hostname"]))
}
