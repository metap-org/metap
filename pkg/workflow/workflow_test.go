package workflow

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/metap-org/metap/pkg/metadata"
	"github.com/metap-org/metap/pkg/permission"
	"github.com/stretchr/testify/require"
)

func entityWithWorkflow() metadata.EntityDefinition {
	return metadata.EntityDefinition{
		Name:      "crm.customers",
		Label:     "Customer",
		TableName: "crm.customers",
		Fields: []metadata.EntityField{
			{Name: "status", Label: "Status", Kind: metadata.KindEnum, EnumValues: []string{"draft", "active"}},
		},
		Workflow: &metadata.EntityWorkflow{
			StateField:     "status",
			InitialState:   "draft",
			TerminalStates: []string{"active"},
			Transitions: []metadata.WorkflowTransition{
				{Action: "activate", From: "draft", To: "active", Label: "Activate"},
			},
		},
	}
}

func workflowTestContext(userID string) *permission.RequestContext {
	return &permission.RequestContext{TenantID: uuid.NewString(), UserID: userID}
}

func TestInitialStatusDefaultsToWorkflowInitialState(t *testing.T) {
	entity := entityWithWorkflow()
	status, ok := GetInitialStatus(entity, JSONObject{})
	require.True(t, ok, "got %q ok=%v", status, ok)
	require.Equal(t, "draft", status, "got %q ok=%v", status, ok)
}

func TestInitialStatusRespectsExplicitValueInData(t *testing.T) {
	entity := entityWithWorkflow()
	data := JSONObject{"status": mustRaw("active")}
	status, ok := GetInitialStatus(entity, data)
	require.True(t, ok, "got %q ok=%v", status, ok)
	require.Equal(t, "active", status, "got %q ok=%v", status, ok)
}

func TestInitialStatusIsNoneWithoutAWorkflow(t *testing.T) {
	entity := entityWithWorkflow()
	entity.Workflow = nil
	_, ok := GetInitialStatus(entity, JSONObject{})
	require.False(t, ok, "expected no initial status")
}

func TestFindTransitionMatchesOnActionAndFromState(t *testing.T) {
	entity := entityWithWorkflow()
	require.NotNil(t, FindTransition(entity, "activate", "draft"), "expected a match")
	require.Nil(t, FindTransition(entity, "activate", "active"), "expected no match for the wrong from state")
	require.Nil(t, FindTransition(entity, "nope", "draft"), "expected no match for an unknown action")
}

func TestGuardLessTransitionAlwaysPasses(t *testing.T) {
	entity := entityWithWorkflow()
	transition := FindTransition(entity, "activate", "draft")
	ctx := workflowTestContext("")
	require.NoError(t, RunGuard(transition, JSONObject{}, ctx))
}

func TestGuardedTransitionEvaluatesThePolicyConditionAgainstData(t *testing.T) {
	cond := permission.AttributeCondition("amount", permission.OpEq, permission.LiteralValue(mustRaw(100)))
	transition := &metadata.WorkflowTransition{
		Action: "approve", From: "draft", To: "approved", Label: "Approve", Guard: &cond,
	}
	ctx := workflowTestContext("")

	data := JSONObject{"amount": mustRaw(100)}
	require.NoError(t, RunGuard(transition, data, ctx), "expected the guard to pass, got")

	data["amount"] = mustRaw(50)
	err := RunGuard(transition, data, ctx)
	require.Error(t, err, "expected a 'condition failed' error, got %v", err)
	require.Contains(t, err.Error(), "condition failed", "expected a 'condition failed' error, got %v", err)

	transition.Guard = nil // sanity: guard-less path still passes regardless of data
	require.NoError(t, RunGuard(transition, data, ctx))
}

func mustRaw(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
