package crud

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/permission"
	"github.com/metap-org/metap/pkg/query"
)

// fakeAccess answers the entity-level question from a set of allowed "verb" strings and records
// what it was asked.
type fakeAccess struct {
	allowed map[string]bool
	asked   []string
	err     error
}

func (f *fakeAccess) decide(verb string) (permission.PermissionDecision, error) {
	f.asked = append(f.asked, verb)
	if f.err != nil {
		return permission.PermissionDecision{}, f.err
	}
	if f.allowed[verb] {
		return permission.Allowed(), nil
	}
	return permission.Forbidden(), nil
}
func (f *fakeAccess) CanReadEntity(context.Context, *permission.RequestContext, string) (permission.PermissionDecision, error) {
	return f.decide("read")
}
func (f *fakeAccess) CanCreateEntity(context.Context, *permission.RequestContext, string) (permission.PermissionDecision, error) {
	return f.decide("create")
}
func (f *fakeAccess) CanUpdateEntity(context.Context, *permission.RequestContext, string) (permission.PermissionDecision, error) {
	return f.decide("update")
}
func (f *fakeAccess) CanDeleteEntity(context.Context, *permission.RequestContext, string) (permission.PermissionDecision, error) {
	return f.decide("delete")
}
func (f *fakeAccess) CanTransitionAction(_ context.Context, _ *permission.RequestContext, _, action string) (permission.PermissionDecision, error) {
	return f.decide("transition:" + action)
}

// recordingBackend notes which operations reached it.
type recordingBackend struct{ reached []string }

func (r *recordingBackend) List(context.Context, string, query.ListInput, *permission.RequestContext) (ListResult, error) {
	r.reached = append(r.reached, "list")
	return ListResult{}, nil
}
func (r *recordingBackend) Get(context.Context, string, uuid.UUID, *permission.RequestContext) (GetResult, error) {
	r.reached = append(r.reached, "get")
	return GetResult{}, nil
}
func (r *recordingBackend) GetMany(context.Context, string, []uuid.UUID, *permission.RequestContext) ([]GetManyResult, error) {
	r.reached = append(r.reached, "getMany")
	return nil, nil
}
func (r *recordingBackend) Create(context.Context, string, JsonObject, *permission.RequestContext, *string) (RecordDto, error) {
	r.reached = append(r.reached, "create")
	return RecordDto{}, nil
}
func (r *recordingBackend) Update(context.Context, string, uuid.UUID, int32, JsonObject, *permission.RequestContext, *string) (RecordDto, error) {
	r.reached = append(r.reached, "update")
	return RecordDto{}, nil
}
func (r *recordingBackend) Transition(context.Context, string, uuid.UUID, string, int32, JsonObject, *permission.RequestContext, *string) (RecordDto, error) {
	r.reached = append(r.reached, "transition")
	return RecordDto{}, nil
}
func (r *recordingBackend) Delete(context.Context, string, uuid.UUID, int32, *permission.RequestContext, *string) (RecordDto, error) {
	r.reached = append(r.reached, "delete")
	return RecordDto{}, nil
}
func (r *recordingBackend) Aggregate(context.Context, string, query.AggregateSpec, *permission.RequestContext) ([]json.RawMessage, error) {
	r.reached = append(r.reached, "aggregate")
	return nil, nil
}

// operations runs every RecordBackend operation once and reports the first error of each by name.
func operations(b RecordBackend) map[string]error {
	ctx, rc, id := context.Background(), &permission.RequestContext{}, uuid.New()
	out := map[string]error{}
	_, out["list"] = b.List(ctx, "e", query.ListInput{}, rc)
	_, out["get"] = b.Get(ctx, "e", id, rc)
	_, out["getMany"] = b.GetMany(ctx, "e", []uuid.UUID{id}, rc)
	_, out["create"] = b.Create(ctx, "e", nil, rc, nil)
	_, out["update"] = b.Update(ctx, "e", id, 1, nil, rc, nil)
	_, out["transition"] = b.Transition(ctx, "e", id, "approve", 1, nil, rc, nil)
	_, out["delete"] = b.Delete(ctx, "e", id, 1, rc, nil)
	_, out["aggregate"] = b.Aggregate(ctx, "e", query.AggregateSpec{}, rc)
	return out
}

func TestGatedRefusesEveryOperationBeforeTheBackendWhenNothingIsAllowed(t *testing.T) {
	backend := &recordingBackend{}
	for name, err := range operations(Gated(backend, &fakeAccess{})) {
		requireServiceError(t, err, http.StatusForbidden, "forbidden")
		_ = name
	}
	require.Empty(t, backend.reached, "a refused operation must not reach the backend")
}

func TestGatedLetsEveryOperationThroughWhenAllowed(t *testing.T) {
	backend := &recordingBackend{}
	access := &fakeAccess{allowed: map[string]bool{
		"read": true, "create": true, "update": true, "delete": true, "transition:approve": true,
	}}
	for name, err := range operations(Gated(backend, access)) {
		require.NoError(t, err, name)
	}
	require.ElementsMatch(t, []string{"list", "get", "getMany", "create", "update", "transition", "delete", "aggregate"}, backend.reached)
}

func TestGatedAsksTheVerbOfEachOperation(t *testing.T) {
	access := &fakeAccess{allowed: map[string]bool{"read": true}}
	operations(Gated(&recordingBackend{}, access))
	require.ElementsMatch(t,
		[]string{"read", "read", "read", "create", "update", "transition:approve", "delete", "read"}, access.asked)
}

func TestGatedAPolicyFailureIsAnErrorNotAnAllow(t *testing.T) {
	backend := &recordingBackend{}
	boom := &ServiceError{Status: http.StatusInternalServerError, Code: "internal_error"}
	for _, err := range operations(Gated(backend, &fakeAccess{err: boom})) {
		require.ErrorIs(t, err, boom)
	}
	require.Empty(t, backend.reached)
}

func TestAServiceAsksTheEntityLevelQuestionUnlessTrusted(t *testing.T) {
	access := &fakeAccess{}
	svc := &CrudService{access: access}
	c := call{rc: &permission.RequestContext{}}

	err := svc.authorizeEntity(context.Background(), c, permission.ActionUpdate, "")
	requireServiceError(t, err, http.StatusForbidden, "forbidden")
	require.Equal(t, []string{"update"}, access.asked)

	trusted := svc.Trusted()
	for _, verb := range []permission.EntityAction{permission.ActionCreate, permission.ActionUpdate, permission.ActionDelete, permission.ActionTransition} {
		require.NoError(t, trusted.authorizeEntity(context.Background(), c, verb, "approve"), verb.String())
	}
	require.Equal(t, []string{"update"}, access.asked, "a trusted service never asked")

	// Trusted is a copy: the original still checks.
	require.Error(t, svc.authorizeEntity(context.Background(), c, permission.ActionCreate, ""))
}
