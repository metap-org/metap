package permission

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
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
	require.False(t, verdictIsRecordDependent(nil, stateCtx("viewer")), "expected false")
}

func TestARecordConditionalAllowAloneIsRecordDependent(t *testing.T) {
	cond := eqCond("resolution", "unlocked")
	policies := []PolicyRow{statePolicy(nil, false, "record", &cond, EffectAllow)}
	require.True(t, verdictIsRecordDependent(policies, stateCtx("viewer")), "expected true")
}

func TestAnUnconditionalAllowSettlesTheVerdictDespiteAConditionalOne(t *testing.T) {
	cond := eqCond("resolution", "unlocked")
	policies := []PolicyRow{
		statePolicy(nil, false, "context", nil, EffectAllow),
		statePolicy(nil, false, "record", &cond, EffectAllow),
	}
	require.False(t, verdictIsRecordDependent(policies, stateCtx("viewer")), "expected false — unconditional allow already settles it")
}

func TestAConditionalDenyOutranksAnUnconditionalAllow(t *testing.T) {
	cond := eqCond("resolution", "locked")
	policies := []PolicyRow{
		statePolicy(nil, false, "context", nil, EffectAllow),
		statePolicy(nil, false, "record", &cond, EffectDeny),
	}
	require.True(t, verdictIsRecordDependent(policies, stateCtx("viewer")), "expected true")
}

func TestAnUnconditionalDenyPinsTheVerdict(t *testing.T) {
	cond := eqCond("resolution", "unlocked")
	policies := []PolicyRow{
		statePolicy(nil, false, "context", nil, EffectDeny),
		statePolicy(nil, false, "record", &cond, EffectAllow),
	}
	require.False(t, verdictIsRecordDependent(policies, stateCtx("viewer")), "expected false")
}

func TestAConditionalPolicyTheCallerFailsTheRoleGateForIsInert(t *testing.T) {
	cond := eqCond("resolution", "unlocked")
	policies := []PolicyRow{statePolicy([]string{"finance"}, true, "record", &cond, EffectAllow)}
	require.False(t, verdictIsRecordDependent(policies, stateCtx("viewer")), "expected false")
}

func TestAContextSubjectConditionIsNotRecordDependentEitherWay(t *testing.T) {
	grantingCond := eqCond("roles", []string{"viewer"})
	granting := []PolicyRow{statePolicy(nil, false, "context", &grantingCond, EffectAllow)}
	require.False(t, verdictIsRecordDependent(granting, stateCtx("viewer")), "expected false")

	nonMatchingCond := eqCond("roles", []string{"finance"})
	nonMatching := []PolicyRow{statePolicy(nil, false, "context", &nonMatchingCond, EffectAllow)}
	require.False(t, verdictIsRecordDependent(nonMatching, stateCtx("viewer")), "expected false")
}
