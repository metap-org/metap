package permission

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
)

func rawJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func testContext(tenantID string, roles []string, hasRoles bool) *RequestContext {
	return &RequestContext{TenantID: tenantID, Roles: roles, HasRoles: hasRoles}
}

func testPolicy(roles []string, hasRoles bool, condition *PolicyCondition, subject string) PolicyRow {
	return testPolicyWithEffect(roles, hasRoles, condition, subject, EffectAllow)
}

func testPolicyWithEffect(roles []string, hasRoles bool, condition *PolicyCondition, subject string, effect PolicyEffect) PolicyRow {
	return PolicyRow{
		ID: uuid.New(), TenantID: uuid.New(), Entity: "crm.customers", Action: "read",
		Subject: subject, Roles: roles, HasRoles: hasRoles, Condition: condition, Effect: effect,
	}
}

func eqCond(attribute string, literal any) PolicyCondition {
	return AttributeCondition(attribute, OpEq, LiteralValue(rawJSON(literal)))
}

func TestRoleGateOpenWhenNoRolesDeclared(t *testing.T) {
	if !RoleGatePassed(nil, false, []string{"sales"}) {
		t.Fatal("expected open")
	}
	if !RoleGatePassed([]string{}, true, nil) {
		t.Fatal("expected open")
	}
}

func TestRoleGateRequiresOverlap(t *testing.T) {
	policyRoles := []string{"sales", "support"}
	if !RoleGatePassed(policyRoles, true, []string{"support"}) {
		t.Fatal("expected pass")
	}
	if RoleGatePassed(policyRoles, true, []string{"ops"}) {
		t.Fatal("expected fail")
	}
	if RoleGatePassed(policyRoles, true, nil) {
		t.Fatal("expected fail")
	}
}

func TestEqAndNeqOperators(t *testing.T) {
	ctx := testContext("t1", nil, false)
	subject := rawJSON(map[string]string{"status": "active"})

	eq := eqCond("status", "active")
	if !EvaluateCondition(eq, subject, ctx).IsPassed() {
		t.Fatal("expected eq to pass")
	}
	neq := AttributeCondition("status", OpNeq, LiteralValue(rawJSON("active")))
	if EvaluateCondition(neq, subject, ctx).IsPassed() {
		t.Fatal("expected neq to fail")
	}
}

func TestInAndNotInOperators(t *testing.T) {
	ctx := testContext("t1", nil, false)
	subject := rawJSON(map[string]string{"region": "eu"})

	inCond := AttributeCondition("region", OpIn, LiteralValue(rawJSON([]string{"eu", "us"})))
	if !EvaluateCondition(inCond, subject, ctx).IsPassed() {
		t.Fatal("expected in to pass")
	}
	notInCond := AttributeCondition("region", OpNotIn, LiteralValue(rawJSON([]string{"apac"})))
	if !EvaluateCondition(notInCond, subject, ctx).IsPassed() {
		t.Fatal("expected notIn to pass")
	}
}

func TestContainsAndNotContainsOperators(t *testing.T) {
	ctx := testContext("t1", nil, false)
	subject := rawJSON(map[string][]string{"tags": {"urgent", "billing"}})

	contains := AttributeCondition("tags", OpContains, LiteralValue(rawJSON("urgent")))
	if !EvaluateCondition(contains, subject, ctx).IsPassed() {
		t.Fatal("expected contains to pass")
	}
	notContains := AttributeCondition("tags", OpNotContains, LiteralValue(rawJSON("archived")))
	if !EvaluateCondition(notContains, subject, ctx).IsPassed() {
		t.Fatal("expected notContains to pass")
	}
	missing := AttributeCondition("tags", OpContains, LiteralValue(rawJSON("archived")))
	if EvaluateCondition(missing, subject, ctx).IsPassed() {
		t.Fatal("expected contains to fail")
	}
}

func TestContainsFailsClosedWhenTheAttributeIsNotAnArray(t *testing.T) {
	ctx := testContext("t1", nil, false)
	subject := rawJSON(map[string]string{"tags": "urgent"})
	cond := AttributeCondition("tags", OpContains, LiteralValue(rawJSON("urgent")))
	if EvaluateCondition(cond, subject, ctx).IsPassed() {
		t.Fatal("a plain string is not an array, must fail closed")
	}
}

func TestOAuthScopeGatingViaContextSubjectAndContains(t *testing.T) {
	ctxWithScope := &RequestContext{TenantID: "t1"}
	ctxWithScope.ContextAttributes = map[string]json.RawMessage{
		"oauthScope": rawJSON([]string{"read:widgets", "write:widgets"}),
	}
	ctxWithoutScope := &RequestContext{TenantID: "t1"}

	requiresReadScope := AttributeCondition("oauthScope", OpContains, LiteralValue(rawJSON("read:widgets")))

	if !EvaluateCondition(requiresReadScope, ctxWithScope.ToValue(), ctxWithScope).IsPassed() {
		t.Fatal("expected pass with scope")
	}
	if EvaluateCondition(requiresReadScope, ctxWithoutScope.ToValue(), ctxWithoutScope).IsPassed() {
		t.Fatal("an ordinary session must not satisfy a scope requirement")
	}
}

func TestGtCorrectlyExcludesANegativeValueThatNeqZeroWouldWronglyAccept(t *testing.T) {
	ctx := testContext("t1", nil, false)
	negative := rawJSON(map[string]int{"debitAmount": -50})
	positive := rawJSON(map[string]int{"debitAmount": 50})

	gtZero := AttributeCondition("debitAmount", OpGt, LiteralValue(rawJSON(0)))
	if EvaluateCondition(gtZero, negative, ctx).IsPassed() {
		t.Fatal("a negative amount must not satisfy 'debitAmount > 0'")
	}
	if !EvaluateCondition(gtZero, positive, ctx).IsPassed() {
		t.Fatal("expected positive amount to pass")
	}

	neqZero := AttributeCondition("debitAmount", OpNeq, LiteralValue(rawJSON(0)))
	if !EvaluateCondition(neqZero, negative, ctx).IsPassed() {
		t.Fatal("demonstrates why Neq 0 was the wrong operator")
	}
}

func TestOrderingOperatorsCoverNumbersAndStrings(t *testing.T) {
	ctx := testContext("t1", nil, false)
	gte := func(v int) PolicyCondition { return AttributeCondition("amount", OpGte, LiteralValue(rawJSON(v))) }
	if !EvaluateCondition(gte(100), rawJSON(map[string]int{"amount": 100}), ctx).IsPassed() {
		t.Fatal("expected gte pass")
	}
	if EvaluateCondition(gte(101), rawJSON(map[string]int{"amount": 100}), ctx).IsPassed() {
		t.Fatal("expected gte fail")
	}

	lte := func(v int) PolicyCondition { return AttributeCondition("amount", OpLte, LiteralValue(rawJSON(v))) }
	if !EvaluateCondition(lte(100), rawJSON(map[string]int{"amount": 100}), ctx).IsPassed() {
		t.Fatal("expected lte pass")
	}
	if EvaluateCondition(lte(99), rawJSON(map[string]int{"amount": 100}), ctx).IsPassed() {
		t.Fatal("expected lte fail")
	}

	lt := AttributeCondition("name", OpLt, LiteralValue(rawJSON("banana")))
	if !EvaluateCondition(lt, rawJSON(map[string]string{"name": "apple"}), ctx).IsPassed() {
		t.Fatal("expected lt pass")
	}
	if EvaluateCondition(lt, rawJSON(map[string]string{"name": "cherry"}), ctx).IsPassed() {
		t.Fatal("expected lt fail")
	}
}

func TestOrderingOperatorWithMismatchedTypesFailsClosed(t *testing.T) {
	ctx := testContext("t1", nil, false)
	cond := AttributeCondition("amount", OpGt, LiteralValue(rawJSON(10)))
	if EvaluateCondition(cond, rawJSON(map[string]string{"amount": "not-a-number"}), ctx).IsPassed() {
		t.Fatal("expected fail closed on type mismatch")
	}
}

func TestFromContextResolvesAgainstCallerContext(t *testing.T) {
	ctx := testContext("tenant-42", nil, false)
	subject := rawJSON(map[string]string{"tenantId": "tenant-42"})
	cond := AttributeCondition("tenantId", OpEq, FromContextValue("tenantId"))
	if !EvaluateCondition(cond, subject, ctx).IsPassed() {
		t.Fatal("expected pass")
	}
}

func TestAllRequiresEveryConditionToPass(t *testing.T) {
	ctx := testContext("t1", nil, false)
	subject := rawJSON(map[string]int{"a": 1, "b": 2})
	cond := AllCondition(eqCond("a", 1), eqCond("b", 99))
	if EvaluateCondition(cond, subject, ctx).IsPassed() {
		t.Fatal("expected fail")
	}
}

func TestAnyPassesIfOneConditionPasses(t *testing.T) {
	ctx := testContext("t1", nil, false)
	subject := rawJSON(map[string]int{"a": 1, "b": 2})
	cond := AnyCondition(eqCond("a", 99), eqCond("b", 2))
	if !EvaluateCondition(cond, subject, ctx).IsPassed() {
		t.Fatal("expected pass")
	}
}

func TestEvaluatePolicyRowFailsClosedWhenRoleGateFails(t *testing.T) {
	ctx := testContext("t1", []string{"sales"}, true)
	row := testPolicy([]string{"ops"}, true, nil, "context")
	if EvaluatePolicyRow(row, ctx, nil, false) {
		t.Fatal("expected false")
	}
}

func TestEvaluatePolicyRowUsesRecordSubjectWhenDeclared(t *testing.T) {
	ctx := testContext("t1", nil, false)
	record := rawJSON(map[string]string{"ownerId": "u1"})
	cond := eqCond("ownerId", "u1")
	row := testPolicy(nil, false, &cond, "record")
	if !EvaluatePolicyRow(row, ctx, record, true) {
		t.Fatal("expected true")
	}
}

func TestPolicyConditionDeserializesFromJSONMatchingTSWireShape(t *testing.T) {
	raw := []byte(`{"attribute":"ownerId","op":"eq","value":{"fromContext":"userId"}}`)
	var cond PolicyCondition
	if err := json.Unmarshal(raw, &cond); err != nil {
		t.Fatal(err)
	}
	if !cond.isLeaf {
		t.Fatal("expected Attribute variant")
	}
}

func TestDenyOverridesAllowEvenWhenAnAllowPolicyAlsoMatches(t *testing.T) {
	ctx := testContext("t1", []string{"sales"}, true)
	allow := testPolicy([]string{"sales"}, true, nil, "context")
	deny := testPolicyWithEffect([]string{"sales"}, true, nil, "context", EffectDeny)
	if EvaluatePolicies([]PolicyRow{allow, deny}, ctx, nil, false) != VerdictDeny {
		t.Fatal("expected deny")
	}
	if EvaluatePolicies([]PolicyRow{deny, allow}, ctx, nil, false) != VerdictDeny {
		t.Fatal("order must not matter")
	}
}

func TestDenyPolicyThatDoesNotMatchTheCallerDoesNotBlockAMatchingAllow(t *testing.T) {
	ctx := testContext("t1", []string{"sales"}, true)
	allow := testPolicy([]string{"sales"}, true, nil, "context")
	denyForSomeoneElse := testPolicyWithEffect([]string{"ops"}, true, nil, "context", EffectDeny)
	if EvaluatePolicies([]PolicyRow{allow, denyForSomeoneElse}, ctx, nil, false) != VerdictAllow {
		t.Fatal("expected allow")
	}
}

func TestDottedAttributePathResolvesIntoAMergedRelationField(t *testing.T) {
	ctx := testContext("t1", nil, false)
	subject := rawJSON(map[string]map[string]string{"project": {"ownerId": "u1"}})
	cond := eqCond("project.ownerId", "u1")
	if !EvaluateCondition(cond, subject, ctx).IsPassed() {
		t.Fatal("expected pass")
	}
}

func TestDottedAttributePathIsNullWhenTheRelationWasNeverMergedIn(t *testing.T) {
	ctx := testContext("t1", nil, false)
	subject := rawJSON(map[string]string{"status": "active"})
	cond := eqCond("project.ownerId", "u1")
	if EvaluateCondition(cond, subject, ctx).IsPassed() {
		t.Fatal("expected fail")
	}
}

func TestRequiredRelationFieldsCollectsDistinctFirstSegmentsAcrossNestedConditions(t *testing.T) {
	deep := AnyCondition(eqCond("project.ownerId", "u1"), eqCond("project.status", "open"))
	cond := AllCondition(deep, eqCond("team.leadId", "u2"), eqCond("status", "active"))
	row := testPolicy(nil, false, &cond, "record")
	fields := RequiredRelationFields([]PolicyRow{row})
	got := map[string]bool{}
	for _, f := range fields {
		got[f] = true
	}
	if len(got) != 2 || !got["project"] || !got["team"] {
		t.Fatalf("got %v", fields)
	}
}

func TestRequiredRelationFieldsIsEmptyWhenNoConditionUsesADottedPath(t *testing.T) {
	cond := eqCond("ownerId", "u1")
	row := testPolicy(nil, false, &cond, "record")
	if len(RequiredRelationFields([]PolicyRow{row})) != 0 {
		t.Fatal("expected empty")
	}
}

func TestNoMatchingPolicyAtAllIsNoMatchNotDeny(t *testing.T) {
	ctx := testContext("t1", []string{"sales"}, true)
	allowForSomeoneElse := testPolicy([]string{"ops"}, true, nil, "context")
	if EvaluatePolicies([]PolicyRow{allowForSomeoneElse}, ctx, nil, false) != VerdictNoMatch {
		t.Fatal("expected NoMatch")
	}
}
