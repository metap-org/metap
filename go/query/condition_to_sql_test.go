package query

// Ported from the Rust metap-query crate's src/condition_to_sql/tests.rs.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/metap-org/metap-go/permission"
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
	if err != nil || ok {
		t.Fatalf("expected no restriction, got ok=%v err=%v", ok, err)
	}
}

func TestNoRowsMeansNoRestriction(t *testing.T) {
	ctx := condTestContext(nil, false)
	params := NewParamBuilder()
	_, ok, err := RecordPolicyWhereClause(nil, ctx, params)
	if err != nil || ok {
		t.Fatalf("expected no restriction, got ok=%v err=%v", ok, err)
	}
}

func TestRoleGateFailureYieldsFalse(t *testing.T) {
	ctx := condTestContext([]string{"sales"}, true)
	params := NewParamBuilder()
	rows := []permission.PolicyRow{condTestPolicy([]string{"ops"}, true, nil)}
	clause, ok, err := RecordPolicyWhereClause(rows, ctx, params)
	if err != nil || !ok || clause != "false" {
		t.Fatalf("expected clause=false, got %q ok=%v err=%v", clause, ok, err)
	}
}

func TestEqConditionOnJsonbFieldProducesExpectedSqlShape(t *testing.T) {
	ctx := condTestContext(nil, false)
	params := NewParamBuilder()
	cond := literalCondition("ownerId", permission.OpEq, "u1")
	sql, err := ConditionToSQL(cond, ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	if sql != "jsonb_extract_path_text(data, $1) = $2" {
		t.Fatalf("got %q", sql)
	}
	if len(params.Params) != 2 {
		t.Fatalf("expected 2 params, got %d", len(params.Params))
	}
}

func TestUuidFieldBindsNativelyWhenValueParsesAsUuid(t *testing.T) {
	ctx := condTestContext(nil, false)
	params := NewParamBuilder()
	cond := literalCondition("createdBy", permission.OpEq, "123e4567-e89b-12d3-a456-426614174000")
	sql, err := ConditionToSQL(cond, ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	if sql != "created_by = $1" {
		t.Fatalf("got %q", sql)
	}
	if !params.Params[0].IsUUID() {
		t.Fatal("expected a native UUID bind")
	}
}

func TestUuidFieldWithNonUuidValueIsAClearError(t *testing.T) {
	ctx := condTestContext(nil, false)
	params := NewParamBuilder()
	cond := literalCondition("createdBy", permission.OpEq, "not-a-uuid")
	if _, err := ConditionToSQL(cond, ctx, params); err == nil {
		t.Fatal("expected an error")
	}
}

func TestGtConditionAgainstANumberCastsBothSidesNumeric(t *testing.T) {
	ctx := condTestContext(nil, false)
	params := NewParamBuilder()
	cond := literalCondition("amount", permission.OpGt, 10000)
	sql, err := ConditionToSQL(cond, ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	if sql != "(jsonb_extract_path_text(data, $1))::numeric > $2::numeric" {
		t.Fatalf("got %q", sql)
	}
}

func TestGtConditionAgainstAStringStaysAPlainTextComparison(t *testing.T) {
	ctx := condTestContext(nil, false)
	params := NewParamBuilder()
	cond := literalCondition("name", permission.OpLt, "banana")
	sql, err := ConditionToSQL(cond, ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	if sql != "jsonb_extract_path_text(data, $1) < $2" {
		t.Fatalf("got %q", sql)
	}
}

func TestGtConditionOnAUuidFieldIsRejected(t *testing.T) {
	ctx := condTestContext(nil, false)
	params := NewParamBuilder()
	cond := literalCondition("createdBy", permission.OpGt, "123e4567-e89b-12d3-a456-426614174000")
	if _, err := ConditionToSQL(cond, ctx, params); err == nil {
		t.Fatal("expected an error")
	}
}

func TestEmptyInListIsAlwaysFalse(t *testing.T) {
	ctx := condTestContext(nil, false)
	params := NewParamBuilder()
	cond := literalCondition("status", permission.OpIn, []string{})
	sql, err := ConditionToSQL(cond, ctx, params)
	if err != nil || sql != "false" {
		t.Fatalf("got %q err=%v", sql, err)
	}
}

func TestEmptyNotInListIsAlwaysTrue(t *testing.T) {
	ctx := condTestContext(nil, false)
	params := NewParamBuilder()
	cond := literalCondition("status", permission.OpNotIn, []string{})
	sql, err := ConditionToSQL(cond, ctx, params)
	if err != nil || sql != "true" {
		t.Fatalf("got %q err=%v", sql, err)
	}
}

func TestDottedCrossRecordAttributeIsAClearErrorNotASilentNoOp(t *testing.T) {
	ctx := condTestContext(nil, false)
	params := NewParamBuilder()
	cond := literalCondition("project.ownerId", permission.OpEq, "u1")
	_, err := ConditionToSQL(cond, ctx, params)
	if err == nil || !strings.Contains(err.Error(), "cross-record") {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := err.(*CrossRecordConditionInListError); !ok {
		t.Fatalf("expected a CrossRecordConditionInListError, got %T: %v", err, err)
	}
}

func TestContainsOnARecordFieldIsAClearErrorNotSilentlyWrongSql(t *testing.T) {
	ctx := condTestContext(nil, false)
	params := NewParamBuilder()
	cond := literalCondition("tags", permission.OpContains, "urgent")
	_, err := ConditionToSQL(cond, ctx, params)
	if err == nil || !strings.Contains(err.Error(), "contains") {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := err.(*UnsupportedContainsConditionInListError); !ok {
		t.Fatalf("expected an UnsupportedContainsConditionInListError, got %T: %v", err, err)
	}
}

func TestDenyRowNarrowsTheWhereClauseWithAndNotInsteadOfWideningItViaOr(t *testing.T) {
	ctx := condTestContext(nil, false)
	params := NewParamBuilder()
	allow := condTestPolicy(nil, false, nil) // unconditional allow
	deny := condTestPolicyWithEffect(nil, false, literalCondition("status", permission.OpEq, "archived"), permission.EffectDeny)
	sql, ok, err := RecordPolicyWhereClause([]permission.PolicyRow{allow, deny}, ctx, params)
	if err != nil || !ok {
		t.Fatalf("err=%v ok=%v", err, ok)
	}
	if !strings.Contains(sql, "AND NOT") {
		t.Fatalf("expected AND NOT, got: %s", sql)
	}
	if strings.Contains(sql, " OR status") {
		t.Fatalf("deny condition must not be OR'd in: %s", sql)
	}
}

func TestDenyOnlyRowsWithNoAllowStillYieldFalseNotUnrestricted(t *testing.T) {
	ctx := condTestContext(nil, false)
	params := NewParamBuilder()
	denyOnly := condTestPolicyWithEffect(nil, false, nil, permission.EffectDeny)
	sql, ok, err := RecordPolicyWhereClause([]permission.PolicyRow{denyOnly}, ctx, params)
	if err != nil || !ok || sql != "false" {
		t.Fatalf("got %q ok=%v err=%v", sql, ok, err)
	}
}
