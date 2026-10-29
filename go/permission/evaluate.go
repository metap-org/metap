package permission

import (
	"encoding/json"
)

func jsonEqual(a, b json.RawMessage) bool {
	var av, bv any
	ea := json.Unmarshal(a, &av)
	eb := json.Unmarshal(b, &bv)
	if ea != nil || eb != nil {
		return string(a) == string(b)
	}
	ab, _ := json.Marshal(av)
	bb, _ := json.Marshal(bv)
	return string(ab) == string(bb)
}

// ordering: 1 = actual > expected, -1 = actual < expected, 0 = equal, nil = not comparable.
func compareOrdering(actual, expected json.RawMessage) *int {
	var an, bn float64
	if json.Unmarshal(actual, &an) == nil && json.Unmarshal(expected, &bn) == nil {
		switch {
		case an > bn:
			return intPtr(1)
		case an < bn:
			return intPtr(-1)
		default:
			return intPtr(0)
		}
	}
	var as, bs string
	if json.Unmarshal(actual, &as) == nil && json.Unmarshal(expected, &bs) == nil {
		switch {
		case as > bs:
			return intPtr(1)
		case as < bs:
			return intPtr(-1)
		default:
			return intPtr(0)
		}
	}
	return nil
}

func intPtr(i int) *int { return &i }

func jsonArray(v json.RawMessage) ([]json.RawMessage, bool) {
	var arr []json.RawMessage
	if err := json.Unmarshal(v, &arr); err != nil {
		return nil, false
	}
	return arr, true
}

func arrayContains(arr []json.RawMessage, v json.RawMessage) bool {
	for _, item := range arr {
		if jsonEqual(item, v) {
			return true
		}
	}
	return false
}

func matchOperator(op ConditionOp, actual, expected json.RawMessage) bool {
	switch op {
	case OpEq:
		return jsonEqual(actual, expected)
	case OpNeq:
		return !jsonEqual(actual, expected)
	case OpIn:
		arr, ok := jsonArray(expected)
		return ok && arrayContains(arr, actual)
	case OpNotIn:
		arr, ok := jsonArray(expected)
		return ok && !arrayContains(arr, actual)
	case OpContains:
		arr, ok := jsonArray(actual)
		return ok && arrayContains(arr, expected)
	case OpNotContains:
		arr, ok := jsonArray(actual)
		return ok && !arrayContains(arr, expected)
	case OpGt:
		o := compareOrdering(actual, expected)
		return o != nil && *o == 1
	case OpGte:
		o := compareOrdering(actual, expected)
		return o != nil && *o >= 0
	case OpLt:
		o := compareOrdering(actual, expected)
		return o != nil && *o == -1
	case OpLte:
		o := compareOrdering(actual, expected)
		return o != nil && *o <= 0
	default:
		return false
	}
}

// RoleGatePassed: no policy roles at all means the gate is open to everyone.
func RoleGatePassed(policyRoles []string, hasPolicyRoles bool, callerRoles []string) bool {
	if !hasPolicyRoles || len(policyRoles) == 0 {
		return true
	}
	for _, cr := range callerRoles {
		for _, pr := range policyRoles {
			if cr == pr {
				return true
			}
		}
	}
	return false
}

func EvaluatePolicyRow(policy PolicyRow, ctx *RequestContext, recordSubject json.RawMessage, hasRecordSubject bool) bool {
	if !RoleGatePassed(policy.Roles, policy.HasRoles, ctx.Roles) {
		return false
	}
	if policy.Condition == nil {
		return true
	}
	var subject json.RawMessage
	if policy.Subject == "record" {
		if hasRecordSubject {
			subject = recordSubject
		} else {
			subject = ctx.ToValue()
		}
	} else {
		subject = ctx.ToValue()
	}
	return EvaluateCondition(*policy.Condition, subject, ctx).IsPassed()
}

// PolicyVerdict: result of evaluating a whole set of policies together, Deny-overrides-Allow.
type PolicyVerdict int

const (
	VerdictAllow PolicyVerdict = iota
	VerdictDeny
	VerdictNoMatch
)

func (v PolicyVerdict) IsAllowed() bool { return v == VerdictAllow }

// EvaluatePolicies aggregates every policy that matches ctx/recordSubject into one verdict: any
// matching Deny wins outright; otherwise Allow if at least one matching Allow; otherwise NoMatch.
func EvaluatePolicies(policies []PolicyRow, ctx *RequestContext, recordSubject json.RawMessage, hasRecordSubject bool) PolicyVerdict {
	anyAllow := false
	for _, policy := range policies {
		if EvaluatePolicyRow(policy, ctx, recordSubject, hasRecordSubject) {
			if policy.Effect == EffectDeny {
				return VerdictDeny
			}
			anyAllow = true
		}
	}
	if anyAllow {
		return VerdictAllow
	}
	return VerdictNoMatch
}

// ConditionResult is the outcome of evaluating one PolicyCondition.
type ConditionResult struct {
	passed bool
	reason string
}

func passed() ConditionResult              { return ConditionResult{passed: true} }
func failed(reason string) ConditionResult { return ConditionResult{passed: false, reason: reason} }

func (r ConditionResult) IsPassed() bool { return r.passed }
func (r ConditionResult) Reason() (string, bool) {
	if r.passed {
		return "", false
	}
	return r.reason, true
}

func opString(op ConditionOp) string { return string(op) }

func EvaluateCondition(condition PolicyCondition, subject json.RawMessage, ctx *RequestContext) ConditionResult {
	switch {
	case condition.All != nil:
		for _, inner := range condition.All {
			result := EvaluateCondition(inner, subject, ctx)
			if !result.IsPassed() {
				return result
			}
		}
		return passed()
	case condition.Any != nil:
		lastFailure := "no condition in 'any' matched"
		for _, inner := range condition.Any {
			result := EvaluateCondition(inner, subject, ctx)
			if result.IsPassed() {
				return passed()
			}
			if reason, ok := result.Reason(); ok {
				lastFailure = reason
			}
		}
		return failed(lastFailure)
	default:
		actual := resolveAttributePath(subject, condition.Attribute)
		expected := ResolveValue(condition.Value, ctx)
		if matchOperator(condition.Op, actual, expected) {
			return passed()
		}
		return failed(
			"condition failed: " + condition.Attribute + " " + opString(condition.Op) + " " +
				string(expected) + " (got " + string(actual) + ")")
	}
}
