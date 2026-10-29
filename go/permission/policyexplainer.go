package permission

import "encoding/json"

// Gate is one of a policy trace entry's role/condition gate outcomes.
type Gate int

const (
	GateOpen Gate = iota
	GatePassed
	GateFailed
)

func (g Gate) String() string {
	switch g {
	case GateOpen:
		return "open"
	case GatePassed:
		return "passed"
	default:
		return "failed"
	}
}

// PolicyTraceEntry is one policy considered during an explain() call, and why.
type PolicyTraceEntry struct {
	PolicyID        string
	Effect          PolicyEffect
	RoleGate        Gate
	ConditionGate   Gate
	ConditionReason string
}

func (e PolicyTraceEntry) matched() bool {
	return e.RoleGate != GateFailed && e.ConditionGate != GateFailed
}

// PolicyExplanation is the full result of ExplainPolicies.
type PolicyExplanation struct {
	Allowed            bool
	PoliciesConsidered []PolicyTraceEntry
}

// ExplainPolicies is a read-only trace of every policy considered and why, for the admin-gated
// policy simulator.
func ExplainPolicies(policyRows []PolicyRow, ctx *RequestContext, subject JsonObject, hasSubject bool) PolicyExplanation {
	if len(policyRows) == 0 {
		return PolicyExplanation{Allowed: true}
	}

	entries := make([]PolicyTraceEntry, 0, len(policyRows))
	for _, row := range policyRows {
		rolePassed := RoleGatePassed(row.Roles, row.HasRoles, ctx.Roles)
		roleGate := GateOpen
		if row.HasRoles && len(row.Roles) > 0 {
			if rolePassed {
				roleGate = GatePassed
			} else {
				roleGate = GateFailed
			}
		}

		if !rolePassed {
			entries = append(entries, PolicyTraceEntry{PolicyID: row.ID.String(), Effect: row.Effect, RoleGate: roleGate, ConditionGate: GateOpen})
			continue
		}
		if row.Condition == nil {
			entries = append(entries, PolicyTraceEntry{PolicyID: row.ID.String(), Effect: row.Effect, RoleGate: roleGate, ConditionGate: GateOpen})
			continue
		}

		var conditionSubject json.RawMessage
		if row.Subject == "record" {
			if hasSubject {
				conditionSubject, _ = json.Marshal(subject)
			} else {
				conditionSubject = ctx.ToValue()
			}
		} else {
			conditionSubject = ctx.ToValue()
		}

		result := EvaluateCondition(*row.Condition, conditionSubject, ctx)
		if result.IsPassed() {
			entries = append(entries, PolicyTraceEntry{PolicyID: row.ID.String(), Effect: row.Effect, RoleGate: roleGate, ConditionGate: GatePassed})
		} else {
			reason, _ := result.Reason()
			entries = append(entries, PolicyTraceEntry{PolicyID: row.ID.String(), Effect: row.Effect, RoleGate: roleGate, ConditionGate: GateFailed, ConditionReason: reason})
		}
	}

	anyMatchingDeny, anyMatchingAllow := false, false
	for _, e := range entries {
		if e.matched() {
			if e.Effect == EffectDeny {
				anyMatchingDeny = true
			} else {
				anyMatchingAllow = true
			}
		}
	}
	allowed := !anyMatchingDeny && anyMatchingAllow

	return PolicyExplanation{Allowed: allowed, PoliciesConsidered: entries}
}
