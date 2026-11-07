package permission

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
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
	require.True(t, RoleGatePassed(nil, false, []string{"sales"}), "expected open")
	require.True(t, RoleGatePassed([]string{}, true, nil), "expected open")
}

func TestRoleGateRequiresOverlap(t *testing.T) {
	policyRoles := []string{"sales", "support"}
	require.True(t, RoleGatePassed(policyRoles, true, []string{"support"}), "expected pass")
	require.False(t, RoleGatePassed(policyRoles, true, []string{"ops"}), "expected fail")
	require.False(t, RoleGatePassed(policyRoles, true, nil), "expected fail")
}

func TestEqAndNeqOperators(t *testing.T) {
	ctx := testContext("t1", nil, false)
	subject := rawJSON(map[string]string{"status": "active"})

	eq := eqCond("status", "active")
	require.True(t, EvaluateCondition(eq, subject, ctx).IsPassed(), "expected eq to pass")
	neq := AttributeCondition("status", OpNeq, LiteralValue(rawJSON("active")))
	require.False(t, EvaluateCondition(neq, subject, ctx).IsPassed(), "expected neq to fail")
}

func TestInAndNotInOperators(t *testing.T) {
	ctx := testContext("t1", nil, false)
	subject := rawJSON(map[string]string{"region": "eu"})

	inCond := AttributeCondition("region", OpIn, LiteralValue(rawJSON([]string{"eu", "us"})))
	require.True(t, EvaluateCondition(inCond, subject, ctx).IsPassed(), "expected in to pass")
	notInCond := AttributeCondition("region", OpNotIn, LiteralValue(rawJSON([]string{"apac"})))
	require.True(t, EvaluateCondition(notInCond, subject, ctx).IsPassed(), "expected notIn to pass")
}

func TestContainsAndNotContainsOperators(t *testing.T) {
	ctx := testContext("t1", nil, false)
	subject := rawJSON(map[string][]string{"tags": {"urgent", "billing"}})

	contains := AttributeCondition("tags", OpContains, LiteralValue(rawJSON("urgent")))
	require.True(t, EvaluateCondition(contains, subject, ctx).IsPassed(), "expected contains to pass")
	notContains := AttributeCondition("tags", OpNotContains, LiteralValue(rawJSON("archived")))
	require.True(t, EvaluateCondition(notContains, subject, ctx).IsPassed(), "expected notContains to pass")
	missing := AttributeCondition("tags", OpContains, LiteralValue(rawJSON("archived")))
	require.False(t, EvaluateCondition(missing, subject, ctx).IsPassed(), "expected contains to fail")
}

func TestContainsFailsClosedWhenTheAttributeIsNotAnArray(t *testing.T) {
	ctx := testContext("t1", nil, false)
	subject := rawJSON(map[string]string{"tags": "urgent"})
	cond := AttributeCondition("tags", OpContains, LiteralValue(rawJSON("urgent")))
	require.False(t, EvaluateCondition(cond, subject, ctx).IsPassed(), "a plain string is not an array, must fail closed")
}

func TestOAuthScopeGatingViaContextSubjectAndContains(t *testing.T) {
	ctxWithScope := &RequestContext{TenantID: "t1"}
	ctxWithScope.ContextAttributes = map[string]json.RawMessage{
		"oauthScope": rawJSON([]string{"read:widgets", "write:widgets"}),
	}
	ctxWithoutScope := &RequestContext{TenantID: "t1"}

	requiresReadScope := AttributeCondition("oauthScope", OpContains, LiteralValue(rawJSON("read:widgets")))

	require.True(t, EvaluateCondition(requiresReadScope, ctxWithScope.ToValue(), ctxWithScope).IsPassed(), "expected pass with scope")
	require.False(t, EvaluateCondition(requiresReadScope, ctxWithoutScope.ToValue(), ctxWithoutScope).IsPassed(), "an ordinary session must not satisfy a scope requirement")
}

func TestGtCorrectlyExcludesANegativeValueThatNeqZeroWouldWronglyAccept(t *testing.T) {
	ctx := testContext("t1", nil, false)
	negative := rawJSON(map[string]int{"debitAmount": -50})
	positive := rawJSON(map[string]int{"debitAmount": 50})

	gtZero := AttributeCondition("debitAmount", OpGt, LiteralValue(rawJSON(0)))
	require.False(t, EvaluateCondition(gtZero, negative, ctx).IsPassed(), "a negative amount must not satisfy 'debitAmount > 0'")
	require.True(t, EvaluateCondition(gtZero, positive, ctx).IsPassed(), "expected positive amount to pass")

	neqZero := AttributeCondition("debitAmount", OpNeq, LiteralValue(rawJSON(0)))
	require.True(t, EvaluateCondition(neqZero, negative, ctx).IsPassed(), "demonstrates why Neq 0 was the wrong operator")
}

func TestOrderingOperatorsCoverNumbersAndStrings(t *testing.T) {
	ctx := testContext("t1", nil, false)
	gte := func(v int) PolicyCondition { return AttributeCondition("amount", OpGte, LiteralValue(rawJSON(v))) }
	require.True(t, EvaluateCondition(gte(100), rawJSON(map[string]int{"amount": 100}), ctx).IsPassed(), "expected gte pass")
	require.False(t, EvaluateCondition(gte(101), rawJSON(map[string]int{"amount": 100}), ctx).IsPassed(), "expected gte fail")

	lte := func(v int) PolicyCondition { return AttributeCondition("amount", OpLte, LiteralValue(rawJSON(v))) }
	require.True(t, EvaluateCondition(lte(100), rawJSON(map[string]int{"amount": 100}), ctx).IsPassed(), "expected lte pass")
	require.False(t, EvaluateCondition(lte(99), rawJSON(map[string]int{"amount": 100}), ctx).IsPassed(), "expected lte fail")

	lt := AttributeCondition("name", OpLt, LiteralValue(rawJSON("banana")))
	require.True(t, EvaluateCondition(lt, rawJSON(map[string]string{"name": "apple"}), ctx).IsPassed(), "expected lt pass")
	require.False(t, EvaluateCondition(lt, rawJSON(map[string]string{"name": "cherry"}), ctx).IsPassed(), "expected lt fail")
}

func TestOrderingOperatorWithMismatchedTypesFailsClosed(t *testing.T) {
	ctx := testContext("t1", nil, false)
	cond := AttributeCondition("amount", OpGt, LiteralValue(rawJSON(10)))
	require.False(t, EvaluateCondition(cond, rawJSON(map[string]string{"amount": "not-a-number"}), ctx).IsPassed(), "expected fail closed on type mismatch")
}

func TestFromContextResolvesAgainstCallerContext(t *testing.T) {
	ctx := testContext("tenant-42", nil, false)
	subject := rawJSON(map[string]string{"tenantId": "tenant-42"})
	cond := AttributeCondition("tenantId", OpEq, FromContextValue("tenantId"))
	require.True(t, EvaluateCondition(cond, subject, ctx).IsPassed(), "expected pass")
}

func TestAllRequiresEveryConditionToPass(t *testing.T) {
	ctx := testContext("t1", nil, false)
	subject := rawJSON(map[string]int{"a": 1, "b": 2})
	cond := AllCondition(eqCond("a", 1), eqCond("b", 99))
	require.False(t, EvaluateCondition(cond, subject, ctx).IsPassed(), "expected fail")
}

func TestAnyPassesIfOneConditionPasses(t *testing.T) {
	ctx := testContext("t1", nil, false)
	subject := rawJSON(map[string]int{"a": 1, "b": 2})
	cond := AnyCondition(eqCond("a", 99), eqCond("b", 2))
	require.True(t, EvaluateCondition(cond, subject, ctx).IsPassed(), "expected pass")
}

func TestEvaluatePolicyRowFailsClosedWhenRoleGateFails(t *testing.T) {
	ctx := testContext("t1", []string{"sales"}, true)
	row := testPolicy([]string{"ops"}, true, nil, "context")
	require.False(t, EvaluatePolicyRow(row, ctx, nil, false), "expected false")
}

func TestEvaluatePolicyRowUsesRecordSubjectWhenDeclared(t *testing.T) {
	ctx := testContext("t1", nil, false)
	record := rawJSON(map[string]string{"ownerId": "u1"})
	cond := eqCond("ownerId", "u1")
	row := testPolicy(nil, false, &cond, "record")
	require.True(t, EvaluatePolicyRow(row, ctx, record, true), "expected true")
}

func TestPolicyConditionDeserializesFromJSONMatchingTSWireShape(t *testing.T) {
	raw := []byte(`{"attribute":"ownerId","op":"eq","value":{"fromContext":"userId"}}`)
	var cond PolicyCondition
	require.NoError(t, json.Unmarshal(raw, &cond))
	require.True(t, cond.isLeaf, "expected Attribute variant")
}

func TestDenyOverridesAllowEvenWhenAnAllowPolicyAlsoMatches(t *testing.T) {
	ctx := testContext("t1", []string{"sales"}, true)
	allow := testPolicy([]string{"sales"}, true, nil, "context")
	deny := testPolicyWithEffect([]string{"sales"}, true, nil, "context", EffectDeny)
	require.Equal(t, VerdictDeny, EvaluatePolicies([]PolicyRow{allow, deny}, ctx, nil, false), "expected deny")
	require.Equal(t, VerdictDeny, EvaluatePolicies([]PolicyRow{deny, allow}, ctx, nil, false), "order must not matter")
}

func TestDenyPolicyThatDoesNotMatchTheCallerDoesNotBlockAMatchingAllow(t *testing.T) {
	ctx := testContext("t1", []string{"sales"}, true)
	allow := testPolicy([]string{"sales"}, true, nil, "context")
	denyForSomeoneElse := testPolicyWithEffect([]string{"ops"}, true, nil, "context", EffectDeny)
	require.Equal(t, VerdictAllow, EvaluatePolicies([]PolicyRow{allow, denyForSomeoneElse}, ctx, nil, false), "expected allow")
}

func TestDottedAttributePathResolvesIntoAMergedRelationField(t *testing.T) {
	ctx := testContext("t1", nil, false)
	subject := rawJSON(map[string]map[string]string{"project": {"ownerId": "u1"}})
	cond := eqCond("project.ownerId", "u1")
	require.True(t, EvaluateCondition(cond, subject, ctx).IsPassed(), "expected pass")
}

func TestDottedAttributePathIsNullWhenTheRelationWasNeverMergedIn(t *testing.T) {
	ctx := testContext("t1", nil, false)
	subject := rawJSON(map[string]string{"status": "active"})
	cond := eqCond("project.ownerId", "u1")
	require.False(t, EvaluateCondition(cond, subject, ctx).IsPassed(), "expected fail")
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
	require.Len(t, got, 2, "got %v", fields)
	require.True(t, got["project"], "got %v", fields)
	require.True(t, got["team"], "got %v", fields)
}

func TestRequiredRelationFieldsIsEmptyWhenNoConditionUsesADottedPath(t *testing.T) {
	cond := eqCond("ownerId", "u1")
	row := testPolicy(nil, false, &cond, "record")
	require.Len(t, RequiredRelationFields([]PolicyRow{row}), 0, "expected empty")
}

func TestNoMatchingPolicyAtAllIsNoMatchNotDeny(t *testing.T) {
	ctx := testContext("t1", []string{"sales"}, true)
	allowForSomeoneElse := testPolicy([]string{"ops"}, true, nil, "context")
	require.Equal(t, VerdictNoMatch, EvaluatePolicies([]PolicyRow{allowForSomeoneElse}, ctx, nil, false), "expected NoMatch")
}
