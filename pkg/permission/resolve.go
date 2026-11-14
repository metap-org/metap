package permission

import (
	"encoding/json"
	"strings"
)

// ResolveValue resolves a PolicyValue against a RequestContext — pub so metap-workflow's
// set_fields.
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

// ContextAttributePrefix makes a condition's attribute read the request context instead of the
// subject: `$ctx.roles contains "approver"`, `$ctx.approvalRequired eq true`. Without it, a
// workflow guard or a record policy (whose subject is the record) could only compare a record
// field against a context value, never test the caller on its own — so "only an approver may
// take this transition" was not expressible in the guard itself. A context-subject policy does
// not need the prefix (its subject already is the context), but accepts it.
const ContextAttributePrefix = "$ctx."

// WorkflowAttributePrefix is the reserved subject key CrudService adds for workflow guards and
// validators (see workflow.WithWorkflowContext): `$workflow.actors.<action>` is the user who most
// recently performed <action> on the record, read from the append-only workflow_events ledger so
// a client can never forge it. Field names cannot start with `$` (compiler.Validate), so neither
// prefix can collide with a real field.
const WorkflowAttributePrefix = "$workflow."

// resolveAttribute resolves a condition's attribute: from the request context under
// ContextAttributePrefix, otherwise as a dotted path into subject.
func resolveAttribute(subject json.RawMessage, attribute string, ctx *RequestContext) json.RawMessage {
	if rest, ok := strings.CutPrefix(attribute, ContextAttributePrefix); ok {
		if ctx == nil {
			return json.RawMessage("null")
		}
		return resolveAttributePath(ctx.ToValue(), rest)
	}
	return resolveAttributePath(subject, attribute)
}

// IsContextAttribute reports whether attribute reads the request context (ContextAttributePrefix)
// rather than the subject — a per-request constant, so a SQL translation can evaluate it up front.
func IsContextAttribute(attribute string) bool {
	return strings.HasPrefix(attribute, ContextAttributePrefix)
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
	// `$ctx.`/`$workflow.` paths are not relation fields: nothing to join for them.
	if strings.HasPrefix(attribute, "$") {
		return "", false
	}
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
