package permission

import (
	"testing"

	"github.com/google/uuid"
)

func explainRow(roles []string, hasRoles bool, effect PolicyEffect) PolicyRow {
	return PolicyRow{
		ID: uuid.New(), TenantID: uuid.New(), Entity: "crm.customers", Action: "read",
		Subject: "context", Roles: roles, HasRoles: hasRoles, Effect: effect,
	}
}

func TestAMatchingDenyOverridesAMatchingAllowAndIsVisibleInTheTrace(t *testing.T) {
	ctx := &RequestContext{TenantID: uuid.NewString(), Roles: []string{"sales"}, HasRoles: true}
	allow := explainRow([]string{"sales"}, true, EffectAllow)
	deny := explainRow([]string{"sales"}, true, EffectDeny)

	explanation := ExplainPolicies([]PolicyRow{allow, deny}, ctx, nil, false)
	if explanation.Allowed {
		t.Fatal("a matching Deny must flip allowed to false")
	}
	var denyEntry *PolicyTraceEntry
	for i := range explanation.PoliciesConsidered {
		if explanation.PoliciesConsidered[i].PolicyID == deny.ID.String() {
			denyEntry = &explanation.PoliciesConsidered[i]
		}
	}
	if denyEntry == nil || denyEntry.Effect != EffectDeny {
		t.Fatal("expected deny entry with Effect == EffectDeny")
	}

	got := EvaluatePolicies([]PolicyRow{allow, deny}, ctx, nil, false).IsAllowed()
	if got != explanation.Allowed {
		t.Fatal("explanation must agree with real enforcement")
	}
}

func TestAllowOnlyIsAllowedAndDenyOnlyIsNot(t *testing.T) {
	ctx := &RequestContext{TenantID: uuid.NewString(), Roles: []string{"sales"}, HasRoles: true}
	allowOnly := ExplainPolicies([]PolicyRow{explainRow([]string{"sales"}, true, EffectAllow)}, ctx, nil, false)
	if !allowOnly.Allowed {
		t.Fatal("expected allowed")
	}
	denyOnly := ExplainPolicies([]PolicyRow{explainRow([]string{"sales"}, true, EffectDeny)}, ctx, nil, false)
	if denyOnly.Allowed {
		t.Fatal("expected not allowed")
	}
}

func TestADenyThatDoesNotMatchTheCallerDoesNotBlockAMatchingAllow(t *testing.T) {
	ctx := &RequestContext{TenantID: uuid.NewString(), Roles: []string{"sales"}, HasRoles: true}
	allow := explainRow([]string{"sales"}, true, EffectAllow)
	denyForSomeoneElse := explainRow([]string{"ops"}, true, EffectDeny)

	explanation := ExplainPolicies([]PolicyRow{allow, denyForSomeoneElse}, ctx, nil, false)
	if !explanation.Allowed {
		t.Fatal("a Deny that doesn't match this caller must not block an Allow that does")
	}
}
