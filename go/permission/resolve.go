package permission

import (
	"encoding/json"
	"strings"
)

// ResolveValue resolves a PolicyValue against a RequestContext — pub so metap-workflow's
// set_fields (ported later) can reuse this exact resolution.
func ResolveValue(value PolicyValue, ctx *RequestContext) json.RawMessage {
	if value.isFromCtx {
		var m map[string]json.RawMessage
		_ = json.Unmarshal(ctx.ToValue(), &m)
		if v, ok := m[value.FromContext]; ok {
			return v
		}
		return json.RawMessage("null")
	}
	if value.Literal == nil {
		return json.RawMessage("null")
	}
	return value.Literal
}

// resolveAttributePath: attribute may be a dotted path to reach into a related record's data
// CrudService has merged onto subject under the relation field's own name.
func resolveAttributePath(subject json.RawMessage, attribute string) json.RawMessage {
	current := subject
	for _, segment := range strings.Split(attribute, ".") {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(current, &m); err != nil {
			return json.RawMessage("null")
		}
		next, ok := m[segment]
		if !ok {
			return json.RawMessage("null")
		}
		current = next
	}
	return current
}

func relationFieldOf(attribute string) (string, bool) {
	head, _, found := strings.Cut(attribute, ".")
	return head, found
}

func collectRelationFields(condition PolicyCondition, out *[]string) {
	switch {
	case condition.isLeaf:
		if field, ok := relationFieldOf(condition.Attribute); ok {
			for _, f := range *out {
				if f == field {
					return
				}
			}
			*out = append(*out, field)
		}
	case condition.All != nil:
		for _, inner := range condition.All {
			collectRelationFields(inner, out)
		}
	case condition.Any != nil:
		for _, inner := range condition.Any {
			collectRelationFields(inner, out)
		}
	}
}

// RequiredRelationFields is every relation field name referenced by a dotted attribute path
// across policies' conditions.
func RequiredRelationFields(policies []PolicyRow) []string {
	var out []string
	for _, policy := range policies {
		if policy.Condition != nil {
			collectRelationFields(*policy.Condition, &out)
		}
	}
	return out
}
