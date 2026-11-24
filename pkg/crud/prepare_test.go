package crud

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/metadata"
	"github.com/metap-org/metap/pkg/permission"
)

// The prepare functions are the part of a write that decides what to store, with no database and
// no permission lookup: a snapshot, a caller and plain values go in; the row to write, or the
// error to answer, comes out.

func prepareEntity() metadata.EntityDefinition {
	required := true
	return metadata.EntityDefinition{
		Name: "test.orders", Label: "Order", TableName: "test.orders",
		Fields: []metadata.EntityField{
			{Name: "name", Label: "Name", Kind: metadata.KindString, Required: &required},
			{Name: "status", Label: "Status", Kind: metadata.KindEnum, EnumValues: []string{"draft", "active"}},
		},
		Workflow: &metadata.EntityWorkflow{
			StateField: "status", InitialState: "draft",
			Transitions: []metadata.WorkflowTransition{{Action: "activate", From: "draft", To: "active", Label: "Activate"}},
		},
	}
}

func prepareAdmin() *permission.RequestContext {
	return &permission.RequestContext{TenantID: uuid.NewString(), UserID: uuid.NewString(), Roles: []string{permission.RoleAdmin}, HasRoles: true}
}

func prepareSnapshot() *permission.PermissionSnapshot {
	return permission.PermissionSnapshotFromRows(nil)
}

func obj(t *testing.T, v map[string]any) JsonObject {
	t.Helper()
	out := JsonObject{}
	for k, val := range v {
		b, err := json.Marshal(val)
		require.NoError(t, err)
		out[k] = b
	}
	return out
}

func asString(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var s string
	require.NoError(t, json.Unmarshal(raw, &s))
	return s
}

func requireServiceError(t *testing.T, err error, status int, code string) *ServiceError {
	t.Helper()
	se, ok := AsServiceError(err)
	require.True(t, ok, "want a ServiceError, got %v", err)
	require.Equal(t, status, int(se.Status))
	require.Equal(t, code, se.Code)
	return se
}

func TestPrepareCreateStartsAWorkflowEntityInItsInitialState(t *testing.T) {
	p, err := prepareCreate(prepareEntity(), prepareSnapshot(), prepareAdmin(), obj(t, map[string]any{"name": "first"}))
	require.NoError(t, err)
	require.NotNil(t, p.status)
	require.Equal(t, "draft", *p.status, "the status column is the workflow's initial state")
	require.Equal(t, "draft", asString(t, p.data["status"]), "and the state field is written into the data so the two agree")
	require.JSONEq(t, string(p.dataJSON), mustMarshal(t, p.data))
}

func TestPrepareCreateRefusesMissingRequiredFieldWithAFieldError(t *testing.T) {
	_, err := prepareCreate(prepareEntity(), prepareSnapshot(), prepareAdmin(), obj(t, map[string]any{}))
	se := requireServiceError(t, err, http.StatusBadRequest, "validation_failed")
	require.Contains(t, se.FieldErrors, "name")
}

func TestPrepareCreateFindsTheRecordCode(t *testing.T) {
	require.Nil(t, codeOf(obj(t, map[string]any{"name": "x"})))
	got := codeOf(obj(t, map[string]any{"code": "ORD-1"}))
	require.NotNil(t, got)
	require.Equal(t, "ORD-1", *got)
	require.Nil(t, codeOf(obj(t, map[string]any{"code": 7})), "a code that is not a string is not a code")
}

func storedOrder(t *testing.T, state string) *RecordDto {
	t.Helper()
	return &RecordDto{ID: uuid.New(), Entity: "test.orders", Data: obj(t, map[string]any{"name": "first", "status": state}), Version: 3}
}

func TestPrepareUpdateKeepsWhatTheCallerDidNotSend(t *testing.T) {
	p, err := prepareUpdate(prepareEntity(), prepareSnapshot(), prepareAdmin(), storedOrder(t, "draft"), obj(t, map[string]any{"name": "renamed"}))
	require.NoError(t, err)
	require.Equal(t, "renamed", asString(t, p.data["name"]))
	require.Equal(t, "draft", asString(t, p.data["status"]))
}

func TestPrepareUpdateCannotMoveTheWorkflowState(t *testing.T) {
	p, err := prepareUpdate(prepareEntity(), prepareSnapshot(), prepareAdmin(), storedOrder(t, "draft"),
		obj(t, map[string]any{"status": "active"}))
	require.NoError(t, err)
	require.Equal(t, "draft", asString(t, p.data["status"]), "only create and transition move the state field")
}

func TestPrepareUpdateValidatesTheMergedRecord(t *testing.T) {
	_, err := prepareUpdate(prepareEntity(), prepareSnapshot(), prepareAdmin(), storedOrder(t, "draft"),
		obj(t, map[string]any{"name": nil}))
	requireServiceError(t, err, http.StatusBadRequest, "validation_failed")
}

func TestPrepareTransitionMovesTheStateAndSaysFromAndTo(t *testing.T) {
	cur := &current{record: storedOrder(t, "draft")}
	p, err := prepareTransition(prepareEntity(), prepareSnapshot(), prepareAdmin(), cur, "activate", nil)
	require.NoError(t, err)
	require.Equal(t, "draft", p.fromState)
	require.Equal(t, "active", p.toState)
	var data map[string]string
	require.NoError(t, json.Unmarshal(p.dataJSON, &data))
	require.Equal(t, "active", data["status"])
	require.Equal(t, "first", data["name"], "the rest of the record is carried over")
}

func TestPrepareTransitionAppliesThePayloadOntoTheRecord(t *testing.T) {
	cur := &current{record: storedOrder(t, "draft")}
	p, err := prepareTransition(prepareEntity(), prepareSnapshot(), prepareAdmin(), cur, "activate", obj(t, map[string]any{"name": "approved"}))
	require.NoError(t, err)
	var data map[string]string
	require.NoError(t, json.Unmarshal(p.dataJSON, &data))
	require.Equal(t, "approved", data["name"])
}

func TestPrepareTransitionRefusesAnActionTheCurrentStateDoesNotHave(t *testing.T) {
	cur := &current{record: storedOrder(t, "active")} // activate goes from draft
	_, err := prepareTransition(prepareEntity(), prepareSnapshot(), prepareAdmin(), cur, "activate", nil)
	requireServiceError(t, err, http.StatusConflict, "invalid_transition")
}

func TestPrepareTransitionOnAnEntityWithoutAWorkflow(t *testing.T) {
	e := prepareEntity()
	e.Workflow = nil
	cur := &current{record: storedOrder(t, "draft")}
	_, err := prepareTransition(e, prepareSnapshot(), prepareAdmin(), cur, "activate", nil)
	requireServiceError(t, err, http.StatusBadRequest, "no_workflow")
}

func mustMarshal(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return string(b)
}
