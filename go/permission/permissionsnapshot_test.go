package permission

import (
	"testing"

	"github.com/google/uuid"
)

func stateCtx(roles ...string) *RequestContext {
	return &RequestContext{TenantID: uuid.NewString(), UserID: uuid.NewString(), Roles: roles, HasRoles: true}
}

func statePolicy(roles []string, hasRoles bool, subject string, condition *PolicyCondition, effect PolicyEffect) PolicyRow {
	return PolicyRow{
		ID: uuid.New(), TenantID: uuid.New(), Entity: "test.orders", Action: "read",
		Subject: subject, Roles: roles, HasRoles: hasRoles, Condition: condition, Effect: effect,
	}
}

func TestAnEmptyPolicySetIsNotRecordDependent(t *testing.T) {
	if verdictIsRecordDependent(nil, stateCtx("viewer")) {
		t.Fatal("expected false")
	}
}

func TestARecordConditionalAllowAloneIsRecordDependent(t *testing.T) {
	cond := eqCond("resolution", "unlocked")
	policies := []PolicyRow{statePolicy(nil, false, "record", &cond, EffectAllow)}
	if !verdictIsRecordDependent(policies, stateCtx("viewer")) {
		t.Fatal("expected true")
	}
}

func TestAnUnconditionalAllowSettlesTheVerdictDespiteAConditionalOne(t *testing.T) {
	cond := eqCond("resolution", "unlocked")
	policies := []PolicyRow{
		statePolicy(nil, false, "context", nil, EffectAllow),
		statePolicy(nil, false, "record", &cond, EffectAllow),
	}
	if verdictIsRecordDependent(policies, stateCtx("viewer")) {
		t.Fatal("expected false — unconditional allow already settles it")
	}
}

func TestAConditionalDenyOutranksAnUnconditionalAllow(t *testing.T) {
	cond := eqCond("resolution", "locked")
	policies := []PolicyRow{
		statePolicy(nil, false, "context", nil, EffectAllow),
		statePolicy(nil, false, "record", &cond, EffectDeny),
	}
	if !verdictIsRecordDependent(policies, stateCtx("viewer")) {
		t.Fatal("expected true")
	}
}

func TestAnUnconditionalDenyPinsTheVerdict(t *testing.T) {
	cond := eqCond("resolution", "unlocked")
	policies := []PolicyRow{
		statePolicy(nil, false, "context", nil, EffectDeny),
		statePolicy(nil, false, "record", &cond, EffectAllow),
	}
	if verdictIsRecordDependent(policies, stateCtx("viewer")) {
		t.Fatal("expected false")
	}
}

func TestAConditionalPolicyTheCallerFailsTheRoleGateForIsInert(t *testing.T) {
	cond := eqCond("resolution", "unlocked")
	policies := []PolicyRow{statePolicy([]string{"finance"}, true, "record", &cond, EffectAllow)}
	if verdictIsRecordDependent(policies, stateCtx("viewer")) {
		t.Fatal("expected false")
	}
}

func TestAContextSubjectConditionIsNotRecordDependentEitherWay(t *testing.T) {
	grantingCond := eqCond("roles", []string{"viewer"})
	granting := []PolicyRow{statePolicy(nil, false, "context", &grantingCond, EffectAllow)}
	if verdictIsRecordDependent(granting, stateCtx("viewer")) {
		t.Fatal("expected false")
	}

	nonMatchingCond := eqCond("roles", []string{"finance"})
	nonMatching := []PolicyRow{statePolicy(nil, false, "context", &nonMatchingCond, EffectAllow)}
	if verdictIsRecordDependent(nonMatching, stateCtx("viewer")) {
		t.Fatal("expected false")
	}
}
