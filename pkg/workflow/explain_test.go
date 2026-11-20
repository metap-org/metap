package workflow

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/metadata"
	"github.com/metap-org/metap/pkg/permission"
)

func zoneLike() *metadata.EntityDefinition {
	return &metadata.EntityDefinition{Fields: []metadata.EntityField{
		{Name: "hasConfig", Label: "Has Config"},
		{Name: "verificationStatus", Label: "Verification Status"},
	}}
}

func activateGuard() permission.PolicyCondition {
	return permission.AllCondition(
		permission.AttributeCondition("hasConfig", permission.OpEq, permission.LiteralValue(json.RawMessage("true"))),
		permission.AttributeCondition("verificationStatus", permission.OpEq, permission.LiteralValue(json.RawMessage(`"verified"`))),
	)
}

// No message declared anywhere: the sentence uses the field's label and says "empty" for null.
func TestExplainBuildsAReadableDefault(t *testing.T) {
	guard := activateGuard()
	transition := metadata.WorkflowTransition{Action: "activate", Guard: &guard}
	err := RunGuard(&transition, JSONObject{"hasConfig": json.RawMessage("true")}, &permission.RequestContext{})
	require.Error(t, err)

	message, details := Explain(err, zoneLike())
	assert.Equal(t, `Verification Status must be "verified" (currently empty)`, message)
	assert.Equal(t, "verificationStatus", details["field"])
	assert.Equal(t, "Verification Status", details["label"])
	assert.Equal(t, "eq", details["op"])
	assert.JSONEq(t, `"verified"`, string(details["expected"].(json.RawMessage)))
	assert.JSONEq(t, `null`, string(details["actual"].(json.RawMessage)))
}

// Each part of a composite guard reports its own message; the first failing part wins.
func TestExplainUsesTheFailingConditionsOwnMessage(t *testing.T) {
	guard := permission.AllCondition(
		permission.AttributeCondition("hasConfig", permission.OpEq, permission.LiteralValue(json.RawMessage("true"))).
			WithMessage("The zone has no protection config yet."),
		permission.AttributeCondition("verificationStatus", permission.OpEq, permission.LiteralValue(json.RawMessage(`"verified"`))).
			WithMessage("The zone's domain is not verified yet."),
	)
	transition := metadata.WorkflowTransition{Action: "activate", Guard: &guard}
	ctx := &permission.RequestContext{}

	err := RunGuard(&transition, JSONObject{"hasConfig": json.RawMessage("true")}, ctx)
	message, details := Explain(err, zoneLike())
	assert.Equal(t, "The zone's domain is not verified yet.", message)
	assert.Equal(t, "verificationStatus", details["field"], "details still name the failing check")

	err = RunGuard(&transition, JSONObject{"hasConfig": json.RawMessage("false")}, ctx)
	message, _ = Explain(err, zoneLike())
	assert.Equal(t, "The zone has no protection config yet.", message)

	// The transition's own GuardMessage still overrides everything.
	transition.GuardMessage = "Not ready to activate."
	err = RunGuard(&transition, JSONObject{}, ctx)
	message, _ = Explain(err, zoneLike())
	assert.Equal(t, "Not ready to activate.", message)
}

// A group's message covers whichever member failed.
func TestExplainGroupMessage(t *testing.T) {
	guard := activateGuard().WithMessage("Verify the domain and add a policy first.")
	transition := metadata.WorkflowTransition{Action: "activate", Guard: &guard}
	err := RunGuard(&transition, JSONObject{}, &permission.RequestContext{})
	message, details := Explain(err, zoneLike())
	assert.Equal(t, "Verify the domain and add a policy first.", message)
	assert.Equal(t, "hasConfig", details["field"])
}
