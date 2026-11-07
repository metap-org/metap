package permission

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
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
	if EvaluateCondition(isApprover, record, ctxWithRoles("author")).IsPassed() {
		t.Fatal("$ctx.roles must read the caller's roles, not a record field named roles")
	}
	if !EvaluateCondition(isApprover, record, ctxWithRoles("author", "approver")).IsPassed() {
		t.Fatal("an approver caller should pass")
	}
	needsApproval := AttributeCondition("$ctx.approvalRequired", OpEq, lit(true))
	if !EvaluateCondition(needsApproval, record, ctxWithRoles()).IsPassed() {
		t.Fatal("$ctx.approvalRequired should read the context attribute")
	}
	if EvaluateCondition(needsApproval, record, nil).IsPassed() {
		t.Fatal("without a context, a $ctx attribute is null")
	}
}

func TestPrefixedAttributesAreNotRelationFields(t *testing.T) {
	policies := []PolicyRow{{Condition: ptr(AllCondition(
		AttributeCondition("$ctx.roles", OpContains, lit("x")),
		AttributeCondition("$workflow.actors.submit", OpNeq, FromContextValue("userId")),
		AttributeCondition("owner.team", OpEq, lit("a")),
	))}}
	got := RequiredRelationFields(policies)
	if len(got) != 1 || got[0] != "owner" {
		t.Fatalf("only real relation fields should be collected, got %v", got)
	}
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
		if got != c.allowed {
			t.Errorf("roles=%v action=%s: got %v, want %v", c.roles, c.action, got, c.allowed)
		}
	}
	if !snapshot.CanPerformEntityAction(ctxWithRoles("lead"), "e", ActionTransition).Allowed {
		t.Error("CanPerformEntityAction should read the snapshot's context policies")
	}
	if snapshot.CanPerformEntityAction(ctxWithRoles("lead"), "e", ActionUpdate).Allowed {
		t.Error("no update policy means no update")
	}
}
