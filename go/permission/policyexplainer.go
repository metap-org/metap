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

// MarshalJSON serializes as the lowercase string the Rust #[derive(Serialize)] Gate enum
// produces — see PolicyEffect.MarshalJSON's own doc comment for why this matters.
func (g Gate) MarshalJSON() ([]byte, error) {
	return json.Marshal(g.String())
}

// PolicyTraceEntry is one policy considered during an explain() call, and why — mirrors the
// Rust PolicyTraceEntry's #[serde(rename_all = "camelCase")] wire shape.
type PolicyTraceEntry struct {
	PolicyID        string       `json:"policyId"`
	Effect          PolicyEffect `json:"effect"`
	RoleGate        Gate         `json:"roleGate"`
	ConditionGate   Gate         `json:"conditionGate"`
	ConditionReason string       `json:"conditionReason,omitempty"`
}

func (e PolicyTraceEntry) matched() bool {
	return e.RoleGate != GateFailed && e.ConditionGate != GateFailed
}

// PolicyExplanation is the full result of ExplainPolicies — same camelCase wire shape as
// PolicyTraceEntry.
type PolicyExplanation struct {
	Allowed            bool               `json:"allowed"`
	PoliciesConsidered []PolicyTraceEntry `json:"policiesConsidered"`
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
