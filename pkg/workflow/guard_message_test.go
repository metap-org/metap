package workflow

import (
	"encoding/json"
	"testing"

	"github.com/metap-org/metap/pkg/metadata"
	"github.com/metap-org/metap/pkg/permission"
	"github.com/stretchr/testify/require"
)

func TestGuardMessageReplacesTheTechnicalReason(t *testing.T) {
	notSubmitter := permission.AttributeCondition("$workflow.actors.submit", permission.OpNeq, permission.FromContextValue("userId"))
	transition := metadata.WorkflowTransition{Action: "approve", From: "pending", To: "done", Guard: &notSubmitter,
		GuardMessage: "The approver must be someone other than the submitter"}
	ctx := &permission.RequestContext{TenantID: "t", UserID: "u1"}

	err := RunGuard(&transition, WithWorkflowContext(JSONObject{}, map[string]string{"submit": "u1"}), ctx)
	require.Error(t, err, "self-approval should fail with the guard message, got %v", err)
	require.Equal(t, transition.GuardMessage, err.Error(), "self-approval should fail with the guard message, got %v", err)
	require.NoError(t, RunGuard(&transition, WithWorkflowContext(JSONObject{}, map[string]string{"submit": "u2"}), ctx), "another user should pass, got")

	transition.GuardMessage = ""
	err = RunGuard(&transition, WithWorkflowContext(JSONObject{}, map[string]string{"submit": "u1"}), ctx)
	require.Error(t, err, "without a message the evaluator's reason is kept, got %v", err)
	require.NotEqual(t, "", err.Error(), "without a message the evaluator's reason is kept, got %v", err)
	require.NotEqual(t, "The approver must be someone other than the submitter", err.Error(), "without a message the evaluator's reason is kept, got %v", err)
}

func TestValidatorMessage(t *testing.T) {
	hasReason := permission.AttributeCondition("reason", permission.OpNeq, permission.LiteralValue(json.RawMessage("null")))
	transition := metadata.WorkflowTransition{Action: "close", Validator: &hasReason, ValidatorMessage: "A reason is required"}
	err := RunValidator(&transition, JSONObject{}, &permission.RequestContext{})
	require.Error(t, err, "got %v", err)
	require.Equal(t, "A reason is required", err.Error(), "got %v", err)
}

func TestUsesWorkflowContextAndWithWorkflowContextDoesNotMutate(t *testing.T) {
	plain := permission.AttributeCondition("amount", permission.OpGt, permission.LiteralValue(json.RawMessage("1")))
	sod := permission.AnyCondition(permission.AttributeCondition("$workflow.actors.submit", permission.OpEq, permission.LiteralValue(json.RawMessage(`"x"`))))
	entity := metadata.EntityDefinition{Workflow: &metadata.EntityWorkflow{Transitions: []metadata.WorkflowTransition{{Action: "a", Guard: &plain}}}}
	require.False(t, UsesWorkflowContext(entity), "a plain guard does not need workflow_events")
	entity.Workflow.Transitions = append(entity.Workflow.Transitions, metadata.WorkflowTransition{Action: "b", Validator: &sod})
	require.True(t, UsesWorkflowContext(entity), "a nested $workflow. attribute in a validator should be detected")
	data := JSONObject{"amount": json.RawMessage("2")}
	_ = WithWorkflowContext(data, nil)
	_, ok := data["$workflow"]
	require.False(t, ok, "WithWorkflowContext must return a copy")
	require.Len(t, data, 1, "WithWorkflowContext must return a copy")
}
