package query

// Ported from the Rust metap-query crate's src/condition_to_sql/tests.rs.

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/metap-org/metap/pkg/permission"
	"github.com/stretchr/testify/require"
)

func condTestContext(roles []string, hasRoles bool) *permission.RequestContext {
	return &permission.RequestContext{
		TenantID: uuid.NewString(),
		UserID:   "u1",
		Roles:    roles,
		HasRoles: hasRoles,
	}
}

func condTestPolicy(roles []string, hasRoles bool, condition *permission.PolicyCondition) permission.PolicyRow {
	return condTestPolicyWithEffect(roles, hasRoles, condition, permission.EffectAllow)
}

func condTestPolicyWithEffect(roles []string, hasRoles bool, condition *permission.PolicyCondition, effect permission.PolicyEffect) permission.PolicyRow {
	return permission.PolicyRow{
		ID:        uuid.New(),
		TenantID:  uuid.New(),
		Entity:    "crm.customers",
		Action:    "read",
		Subject:   "record",
		Roles:     roles,
		HasRoles:  hasRoles,
		Condition: condition,
		Effect:    effect,
	}
}

func literalCondition(attribute string, op permission.ConditionOp, literal any) *permission.PolicyCondition {
	b, _ := json.Marshal(literal)
	c := permission.AttributeCondition(attribute, op, permission.LiteralValue(b))
	return &c
}

func TestAdminBypassesRecordPolicyWhereClause(t *testing.T) {
	ctx := condTestContext([]string{"admin"}, true)
	params := NewParamBuilder()
	rows := []permission.PolicyRow{condTestPolicy(nil, false, nil)}
	_, ok, err := RecordPolicyWhereClause(rows, ctx, params)
	require.NoError(t, err, "expected no restriction, got ok=%v err=", ok)
	require.False(t, ok, "expected no restriction, got ok=%v err=%v", ok, err)
}

func TestNoRowsMeansNoRestriction(t *testing.T) {
	ctx := condTestContext(nil, false)
	params := NewParamBuilder()
	_, ok, err := RecordPolicyWhereClause(nil, ctx, params)
	require.NoError(t, err, "expected no restriction, got ok=%v err=", ok)
	require.False(t, ok, "expected no restriction, got ok=%v err=%v", ok, err)
}

func TestRoleGateFailureYieldsFalse(t *testing.T) {
	ctx := condTestContext([]string{"sales"}, true)
	params := NewParamBuilder()
	rows := []permission.PolicyRow{condTestPolicy([]string{"ops"}, true, nil)}
	clause, ok, err := RecordPolicyWhereClause(rows, ctx, params)
	require.NoError(t, err, "expected clause=false, got %q ok=%v err=", clause, ok)
	require.True(t, ok, "expected clause=false, got %q ok=%v err=%v", clause, ok, err)
	require.Equal(t, "false", clause, "expected clause=false, got %q ok=%v err=%v", clause, ok, err)
}

func TestEqConditionOnJsonbFieldProducesExpectedSqlShape(t *testing.T) {
	ctx := condTestContext(nil, false)
	params := NewParamBuilder()
	cond := literalCondition("ownerId", permission.OpEq, "u1")
	sql, err := ConditionToSQL(cond, ctx, params)
	require.NoError(t, err)
	require.Equal(t, "jsonb_extract_path_text(data, $1) = $2", sql, "got %q", sql)
	require.Len(t, params.Params, 2, "expected 2 params, got %d", len(params.Params))
}

func TestUuidFieldBindsNativelyWhenValueParsesAsUuid(t *testing.T) {
	ctx := condTestContext(nil, false)
	params := NewParamBuilder()
	cond := literalCondition("createdBy", permission.OpEq, "123e4567-e89b-12d3-a456-426614174000")
	sql, err := ConditionToSQL(cond, ctx, params)
	require.NoError(t, err)
	require.Equal(t, "created_by = $1", sql, "got %q", sql)
	require.True(t, params.Params[0].IsUUID(), "expected a native UUID bind")
}

func TestUuidFieldWithNonUuidValueIsAClearError(t *testing.T) {
	ctx := condTestContext(nil, false)
	params := NewParamBuilder()
	cond := literalCondition("createdBy", permission.OpEq, "not-a-uuid")
	_, err := ConditionToSQL(cond, ctx, params)
	require.Error(t, err, "expected an error")
}

func TestGtConditionAgainstANumberCastsBothSidesNumeric(t *testing.T) {
	ctx := condTestContext(nil, false)
	params := NewParamBuilder()
	cond := literalCondition("amount", permission.OpGt, 10000)
	sql, err := ConditionToSQL(cond, ctx, params)
	require.NoError(t, err)
	require.Equal(t, "(jsonb_extract_path_text(data, $1))::numeric > $2::numeric", sql, "got %q", sql)
}

func TestGtConditionAgainstAStringStaysAPlainTextComparison(t *testing.T) {
	ctx := condTestContext(nil, false)
	params := NewParamBuilder()
	cond := literalCondition("name", permission.OpLt, "banana")
	sql, err := ConditionToSQL(cond, ctx, params)
	require.NoError(t, err)
	require.Equal(t, "jsonb_extract_path_text(data, $1) < $2", sql, "got %q", sql)
}

func TestGtConditionOnAUuidFieldIsRejected(t *testing.T) {
	ctx := condTestContext(nil, false)
	params := NewParamBuilder()
	cond := literalCondition("createdBy", permission.OpGt, "123e4567-e89b-12d3-a456-426614174000")
	_, err := ConditionToSQL(cond, ctx, params)
	require.Error(t, err, "expected an error")
}

func TestEmptyInListIsAlwaysFalse(t *testing.T) {
	ctx := condTestContext(nil, false)
	params := NewParamBuilder()
	cond := literalCondition("status", permission.OpIn, []string{})
	sql, err := ConditionToSQL(cond, ctx, params)
	require.NoError(t, err, "got %q err=", sql)
	require.Equal(t, "false", sql, "got %q err=%v", sql, err)
}

func TestEmptyNotInListIsAlwaysTrue(t *testing.T) {
	ctx := condTestContext(nil, false)
	params := NewParamBuilder()
	cond := literalCondition("status", permission.OpNotIn, []string{})
	sql, err := ConditionToSQL(cond, ctx, params)
	require.NoError(t, err, "got %q err=", sql)
	require.Equal(t, "true", sql, "got %q err=%v", sql, err)
}

func TestDottedCrossRecordAttributeIsAClearErrorNotASilentNoOp(t *testing.T) {
	ctx := condTestContext(nil, false)
	params := NewParamBuilder()
	cond := literalCondition("project.ownerId", permission.OpEq, "u1")
	_, err := ConditionToSQL(cond, ctx, params)
	require.Error(t, err, "unexpected error: %v", err)
	require.Contains(t, err.Error(), "cross-record", "unexpected error: %v", err)
	_, ok := err.(*CrossRecordConditionInListError)
	require.True(t, ok, "expected a CrossRecordConditionInListError, got %T: %v", err, err)
}

func TestContainsOnARecordFieldIsAClearErrorNotSilentlyWrongSql(t *testing.T) {
	ctx := condTestContext(nil, false)
	params := NewParamBuilder()
	cond := literalCondition("tags", permission.OpContains, "urgent")
	_, err := ConditionToSQL(cond, ctx, params)
	require.Error(t, err, "unexpected error: %v", err)
	require.Contains(t, err.Error(), "contains", "unexpected error: %v", err)
	_, ok := err.(*UnsupportedContainsConditionInListError)
	require.True(t, ok, "expected an UnsupportedContainsConditionInListError, got %T: %v", err, err)
}

func TestDenyRowNarrowsTheWhereClauseWithAndNotInsteadOfWideningItViaOr(t *testing.T) {
	ctx := condTestContext(nil, false)
	params := NewParamBuilder()
	allow := condTestPolicy(nil, false, nil) // unconditional allow
	deny := condTestPolicyWithEffect(nil, false, literalCondition("status", permission.OpEq, "archived"), permission.EffectDeny)
	sql, ok, err := RecordPolicyWhereClause([]permission.PolicyRow{allow, deny}, ctx, params)
	require.NoError(t, err, "err=%v ok=%v", err, ok)
	require.True(t, ok, "err=%v ok=%v", err, ok)
	require.Contains(t, sql, "AND NOT", "expected AND NOT, got: %s", sql)
	require.NotContains(t, sql, " OR status", "deny condition must not be OR'd in: %s", sql)
}

func TestDenyOnlyRowsWithNoAllowStillYieldFalseNotUnrestricted(t *testing.T) {
	ctx := condTestContext(nil, false)
	params := NewParamBuilder()
	denyOnly := condTestPolicyWithEffect(nil, false, nil, permission.EffectDeny)
	sql, ok, err := RecordPolicyWhereClause([]permission.PolicyRow{denyOnly}, ctx, params)
	require.NoError(t, err, "got %q ok=%v err=", sql, ok)
	require.True(t, ok, "got %q ok=%v err=%v", sql, ok, err)
	require.Equal(t, "false", sql, "got %q ok=%v err=%v", sql, ok, err)
}

func TestContextAttributeFoldsToAConstant(t *testing.T) {
	approver := &permission.RequestContext{TenantID: uuid.NewString(), UserID: "u1", Roles: []string{"approver"}, HasRoles: true}
	cond := literalCondition("$ctx.roles", permission.OpContains, "approver")
	params := NewParamBuilder()
	sql, err := ConditionToSQL(cond, approver, params)
	require.NoError(t, err, "got %q, %v; want true", sql, err)
	require.Equal(t, "true", sql, "got %q, %v; want true", sql, err)
	other := &permission.RequestContext{TenantID: uuid.NewString(), UserID: "u2", Roles: []string{"author"}, HasRoles: true}
	sql, err = ConditionToSQL(cond, other, NewParamBuilder())
	require.NoError(t, err, "got %q, %v; want false", sql, err)
	require.Equal(t, "false", sql, "got %q, %v; want false", sql, err)
}

func TestWorkflowAttributeIsRejectedInRecordPolicies(t *testing.T) {
	cond := literalCondition("$workflow.actors.submit", permission.OpEq, "u1")
	_, err := ConditionToSQL(cond, condTestContext(nil, false), NewParamBuilder())
	require.Error(t, err, "expected a $workflow. error, got %v", err)
	require.Contains(t, err.Error(), "$workflow.", "expected a $workflow. error, got %v", err)
}
