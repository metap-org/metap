// Package permission is the RBAC/ABAC engine — ported from metap's Rust metap-permission crate:
// PolicyCondition evaluation, PermissionSnapshot (field/record-level masking), PermissionService
// (entity-level deny-by-default checks), and the policy explainer. PolicyStore is an interface;
// the real Postgres-backed implementation lives wherever Router/tenant-DB access lives (ported
// in a later wave), same layering reason the Rust crate keeps PostgresPolicyStore out of here.
package permission

import (
	"encoding/json"
	"fmt"
)

// ConditionOp is one of PolicyCondition's comparison operators.
type ConditionOp string

const (
	OpEq          ConditionOp = "eq"
	OpNeq         ConditionOp = "neq"
	OpIn          ConditionOp = "in"
	OpNotIn       ConditionOp = "notIn"
	OpGt          ConditionOp = "gt"
	OpGte         ConditionOp = "gte"
	OpLt          ConditionOp = "lt"
	OpLte         ConditionOp = "lte"
	OpContains    ConditionOp = "contains"
	OpNotContains ConditionOp = "notContains"
)

// PolicyValue is either a literal JSON value or a fromContext lookup — mirrors the Rust untagged
// enum.
type PolicyValue struct {
	Literal     json.RawMessage
	FromContext string
	isFromCtx   bool
}

func LiteralValue(v json.RawMessage) PolicyValue { return PolicyValue{Literal: v} }
func FromContextValue(key string) PolicyValue    { return PolicyValue{FromContext: key, isFromCtx: true} }

func (v PolicyValue) IsFromContext() bool { return v.isFromCtx }

func (v PolicyValue) MarshalJSON() ([]byte, error) {
	if v.isFromCtx {
		return json.Marshal(map[string]string{"fromContext": v.FromContext})
	}
	return json.Marshal(map[string]json.RawMessage{"literal": v.Literal})
}

func (v *PolicyValue) UnmarshalJSON(data []byte) error {
	var probe struct {
		FromContext *string         `json:"fromContext"`
		Literal     json.RawMessage `json:"literal"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return err
	}
	if probe.FromContext != nil {
		*v = PolicyValue{FromContext: *probe.FromContext, isFromCtx: true}
		return nil
	}
	*v = PolicyValue{Literal: probe.Literal}
	return nil
}

// PolicyCondition is the declarative shape a PolicyRow's condition JSON deserializes into:
// exactly one of Attribute (with Op/Value set), All, or Any.
type PolicyCondition struct {
	// Attribute kind:
	Attribute string
	Op        ConditionOp
	Value     PolicyValue
	isLeaf    bool

	All []PolicyCondition
	Any []PolicyCondition
}

func AttributeCondition(attribute string, op ConditionOp, value PolicyValue) PolicyCondition {
	return PolicyCondition{Attribute: attribute, Op: op, Value: value, isLeaf: true}
}
func AllCondition(conditions ...PolicyCondition) PolicyCondition {
	return PolicyCondition{All: conditions}
}
func AnyCondition(conditions ...PolicyCondition) PolicyCondition {
	return PolicyCondition{Any: conditions}
}

func (c PolicyCondition) MarshalJSON() ([]byte, error) {
	switch {
	case c.isLeaf:
		return json.Marshal(struct {
			Attribute string      `json:"attribute"`
			Op        ConditionOp `json:"op"`
			Value     PolicyValue `json:"value"`
		}{c.Attribute, c.Op, c.Value})
	case c.All != nil:
		return json.Marshal(struct {
			All []PolicyCondition `json:"all"`
		}{c.All})
	case c.Any != nil:
		return json.Marshal(struct {
			Any []PolicyCondition `json:"any"`
		}{c.Any})
	default:
		return nil, fmt.Errorf("permission: PolicyCondition has no variant set")
	}
}

func (c *PolicyCondition) UnmarshalJSON(data []byte) error {
	var probe struct {
		Attribute *string           `json:"attribute"`
		Op        ConditionOp       `json:"op"`
		Value     *PolicyValue      `json:"value"`
		All       []PolicyCondition `json:"all"`
		Any       []PolicyCondition `json:"any"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return err
	}
	switch {
	case probe.Attribute != nil:
		*c = PolicyCondition{Attribute: *probe.Attribute, Op: probe.Op, Value: *probe.Value, isLeaf: true}
	case probe.All != nil:
		*c = PolicyCondition{All: probe.All}
	case probe.Any != nil:
		*c = PolicyCondition{Any: probe.Any}
	default:
		return fmt.Errorf("permission: PolicyCondition JSON matches no known variant")
	}
	return nil
}
