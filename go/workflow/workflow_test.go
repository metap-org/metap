package workflow

// Ported from the Rust metap-workflow crate's src/lib.rs test module.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/metap-org/metap-go/metadata"
	"github.com/metap-org/metap-go/permission"
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
	if !ok || status != "draft" {
		t.Fatalf("got %q ok=%v", status, ok)
	}
}

func TestInitialStatusRespectsExplicitValueInData(t *testing.T) {
	entity := entityWithWorkflow()
	data := JSONObject{"status": mustRaw("active")}
	status, ok := GetInitialStatus(entity, data)
	if !ok || status != "active" {
		t.Fatalf("got %q ok=%v", status, ok)
	}
}

func TestInitialStatusIsNoneWithoutAWorkflow(t *testing.T) {
	entity := entityWithWorkflow()
	entity.Workflow = nil
	_, ok := GetInitialStatus(entity, JSONObject{})
	if ok {
		t.Fatal("expected no initial status")
	}
}

func TestFindTransitionMatchesOnActionAndFromState(t *testing.T) {
	entity := entityWithWorkflow()
	if FindTransition(entity, "activate", "draft") == nil {
		t.Fatal("expected a match")
	}
	if FindTransition(entity, "activate", "active") != nil {
		t.Fatal("expected no match for the wrong from state")
	}
	if FindTransition(entity, "nope", "draft") != nil {
		t.Fatal("expected no match for an unknown action")
	}
}

func TestGuardLessTransitionAlwaysPasses(t *testing.T) {
	entity := entityWithWorkflow()
	transition := FindTransition(entity, "activate", "draft")
	ctx := workflowTestContext("")
	if err := RunGuard(transition, JSONObject{}, ctx); err != nil {
		t.Fatal(err)
	}
}

func TestGuardedTransitionEvaluatesThePolicyConditionAgainstData(t *testing.T) {
	cond := permission.AttributeCondition("amount", permission.OpEq, permission.LiteralValue(mustRaw(100)))
	transition := &metadata.WorkflowTransition{
		Action: "approve", From: "draft", To: "approved", Label: "Approve", Guard: &cond,
	}
	ctx := workflowTestContext("")

	data := JSONObject{"amount": mustRaw(100)}
	if err := RunGuard(transition, data, ctx); err != nil {
		t.Fatalf("expected the guard to pass, got %v", err)
	}

	data["amount"] = mustRaw(50)
	err := RunGuard(transition, data, ctx)
	if err == nil || !strings.Contains(err.Error(), "condition failed") {
		t.Fatalf("expected a 'condition failed' error, got %v", err)
	}

	transition.Guard = nil // sanity: guard-less path still passes regardless of data
	if err := RunGuard(transition, data, ctx); err != nil {
		t.Fatal(err)
	}
}

func mustRaw(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
