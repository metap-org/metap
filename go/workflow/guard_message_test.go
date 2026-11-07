package workflow

import (
	"encoding/json"
	"testing"

	"github.com/metap-org/metap-go/metadata"
	"github.com/metap-org/metap-go/permission"
)

func TestGuardMessageReplacesTheTechnicalReason(t *testing.T) {
	notSubmitter := permission.AttributeCondition("$workflow.actors.submit", permission.OpNeq, permission.FromContextValue("userId"))
	transition := metadata.WorkflowTransition{Action: "approve", From: "pending", To: "done", Guard: &notSubmitter,
		GuardMessage: "The approver must be someone other than the submitter"}
	ctx := &permission.RequestContext{TenantID: "t", UserID: "u1"}

	err := RunGuard(&transition, WithWorkflowContext(JSONObject{}, map[string]string{"submit": "u1"}), ctx)
	if err == nil || err.Error() != transition.GuardMessage {
		t.Fatalf("self-approval should fail with the guard message, got %v", err)
	}
	if err := RunGuard(&transition, WithWorkflowContext(JSONObject{}, map[string]string{"submit": "u2"}), ctx); err != nil {
		t.Fatalf("another user should pass, got %v", err)
	}

	transition.GuardMessage = ""
	err = RunGuard(&transition, WithWorkflowContext(JSONObject{}, map[string]string{"submit": "u1"}), ctx)
	if err == nil || err.Error() == "" || err.Error() == "The approver must be someone other than the submitter" {
		t.Fatalf("without a message the evaluator's reason is kept, got %v", err)
	}
}

func TestValidatorMessage(t *testing.T) {
	hasReason := permission.AttributeCondition("reason", permission.OpNeq, permission.LiteralValue(json.RawMessage("null")))
	transition := metadata.WorkflowTransition{Action: "close", Validator: &hasReason, ValidatorMessage: "A reason is required"}
	if err := RunValidator(&transition, JSONObject{}, &permission.RequestContext{}); err == nil || err.Error() != "A reason is required" {
		t.Fatalf("got %v", err)
	}
}

func TestUsesWorkflowContextAndWithWorkflowContextDoesNotMutate(t *testing.T) {
	plain := permission.AttributeCondition("amount", permission.OpGt, permission.LiteralValue(json.RawMessage("1")))
	sod := permission.AnyCondition(permission.AttributeCondition("$workflow.actors.submit", permission.OpEq, permission.LiteralValue(json.RawMessage(`"x"`))))
	entity := metadata.EntityDefinition{Workflow: &metadata.EntityWorkflow{Transitions: []metadata.WorkflowTransition{{Action: "a", Guard: &plain}}}}
	if UsesWorkflowContext(entity) {
		t.Fatal("a plain guard does not need workflow_events")
	}
	entity.Workflow.Transitions = append(entity.Workflow.Transitions, metadata.WorkflowTransition{Action: "b", Validator: &sod})
	if !UsesWorkflowContext(entity) {
		t.Fatal("a nested $workflow. attribute in a validator should be detected")
	}
	data := JSONObject{"amount": json.RawMessage("2")}
	_ = WithWorkflowContext(data, nil)
	if _, ok := data["$workflow"]; ok || len(data) != 1 {
		t.Fatal("WithWorkflowContext must return a copy")
	}
}
