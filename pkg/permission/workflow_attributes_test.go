package permission

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func ctxWithRoles(roles ...string) *RequestContext {
	return &RequestContext{TenantID: uuid.NewString(), UserID: "u1", Roles: roles, HasRoles: true,
		ContextAttributes: map[string]json.RawMessage{"approvalRequired": json.RawMessage("true")}}
}

func lit(v any) PolicyValue {
	b, _ := json.Marshal(v)
	return LiteralValue(b)
}

func TestContextAttributeReadsTheCallerNotTheSubject(t *testing.T) {
	record := json.RawMessage(`{"roles":["approver"],"approvalRequired":false}`)
	isApprover := AttributeCondition("$ctx.roles", OpContains, lit("approver"))
	require.False(t, EvaluateCondition(isApprover, record, ctxWithRoles("author")).IsPassed(), "$ctx.roles must read the caller's roles, not a record field named roles")
	require.True(t, EvaluateCondition(isApprover, record, ctxWithRoles("author", "approver")).IsPassed(), "an approver caller should pass")
	needsApproval := AttributeCondition("$ctx.approvalRequired", OpEq, lit(true))
	require.True(t, EvaluateCondition(needsApproval, record, ctxWithRoles()).IsPassed(), "$ctx.approvalRequired should read the context attribute")
	require.False(t, EvaluateCondition(needsApproval, record, nil).IsPassed(), "without a context, a $ctx attribute is null")
}

func TestPrefixedAttributesAreNotRelationFields(t *testing.T) {
	policies := []PolicyRow{{Condition: ptr(AllCondition(
		AttributeCondition("$ctx.roles", OpContains, lit("x")),
		AttributeCondition("$workflow.actors.submit", OpNeq, FromContextValue("userId")),
		AttributeCondition("owner.team", OpEq, lit("a")),
	))}}
	got := RequiredRelationFields(policies)
	require.Len(t, got, 1, "only real relation fields should be collected, got %v", got)
	require.Equal(t, "owner", got[0], "only real relation fields should be collected, got %v", got)
}

func ptr[T any](v T) *T { return &v }

func transitionPolicy(action string, effect PolicyEffect, roles ...string) PolicyRow {
	return PolicyRow{ID: uuid.New(), Entity: "e", Action: action, Subject: "context", Roles: roles, HasRoles: true, Effect: effect}
}

func TestPerActionTransitionPolicies(t *testing.T) {
	snapshot := PermissionSnapshotFromRows([]PolicyRow{
		transitionPolicy(TransitionActionPolicy("submit"), EffectAllow, "author"),
		transitionPolicy(ActionTransition.String(), EffectAllow, "lead"),
		transitionPolicy(TransitionActionPolicy("approve"), EffectDeny, "lead"),
	})
	cases := []struct {
		roles   []string
		action  string
		allowed bool
	}{
		{[]string{"author"}, "submit", true},   // per-action grant
		{[]string{"author"}, "approve", false}, // no grant for this action
		{[]string{"lead"}, "submit", true},     // plain `transition` covers every action
		{[]string{"lead"}, "approve", false},   // ...but an explicit per-action deny wins
		{[]string{"admin"}, "approve", true},   // admin bypass, as for every entity action
		{[]string{"viewer"}, "submit", false},  // default deny
	}
	for _, c := range cases {
		got := snapshot.CanTransitionAction(ctxWithRoles(c.roles...), "e", c.action).Allowed
		assert.Equal(t, c.allowed, got, "roles=%v action=%s: got %v, want %v", c.roles, c.action, got, c.allowed)
	}
	assert.True(t, snapshot.CanPerformEntityAction(ctxWithRoles("lead"), "e", ActionTransition).Allowed, "CanPerformEntityAction should read the snapshot's context policies")
	assert.False(t, snapshot.CanPerformEntityAction(ctxWithRoles("lead"), "e", ActionUpdate).Allowed, "no update policy means no update")
}
