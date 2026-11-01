// The one deliberate deviation from the original: comparisons are typed per-column (UUID vs
// timestamptz vs text) and fallible, instead of relying on implicit text-parameter type
// inference. A policy condition comparing createdBy/updatedBy to a non-UUID-shaped value, or a
// createdAt/updatedAt condition to an unparseable timestamp, is a clear error at query-build
// time rather than a silent wrong-type comparison.
package query

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/google/uuid"
	"github.com/metap-org/metap-go/permission"
)

type colType int

const (
	colUUID colType = iota
	colText
	colTimestamptz
)

// CrossRecordConditionInListError: a record-level policy's condition uses a dotted cross-record
// attribute path (e.g. "project.ownerId") that List can't resolve. Deterministic and permanent
// for as long as the policy stays configured this way.
type CrossRecordConditionInListError struct{ Message string }

func (e *CrossRecordConditionInListError) Error() string { return e.Message }

// UnsupportedContainsConditionInListError: a record-level policy's condition uses Contains/
// NotContains — refused rather than silently mistranslated. See conditionToSQL's switch arm.
type UnsupportedContainsConditionInListError struct{ Message string }

func (e *UnsupportedContainsConditionInListError) Error() string { return e.Message }

// fieldExpression: a dotted attribute path names a cross-record condition that list()'s
// SQL-generation path has no equivalent for (would need JOIN support, not built) — rejected
// explicitly rather than silently looking up a JSONB key that can never exist (which would make
// an eq condition never match and a deny condition never fire — wrong in the unsafe direction).
func fieldExpression(fieldName string, params *ParamBuilder) (string, colType, error) {
	for _, c := range fieldName {
		if c == '.' {
			return "", 0, &CrossRecordConditionInListError{Message: fmt.Sprintf(
				"policy condition attribute %q is a cross-record path, not supported in "+
					"list() queries — cross-record conditions only apply to single-record operations "+
					"(get/update/delete/transition)", fieldName)}
		}
	}
	switch fieldName {
	case "createdBy":
		return "created_by", colUUID, nil
	case "updatedBy":
		return "updated_by", colUUID, nil
	case "status":
		return "status", colText, nil
	case "createdAt":
		return "created_at", colTimestamptz, nil
	case "updatedAt":
		return "updated_at", colTimestamptz, nil
	default:
		ph := params.Push(TextBind(fieldName))
		return "jsonb_extract_path_text(data, " + ph + ")", colText, nil
	}
}

func resolveValue(value permission.PolicyValue, context *permission.RequestContext) json.RawMessage {
	if value.IsFromContext() {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(context.ToValue(), &obj); err == nil {
			if v, ok := obj[value.FromContext]; ok {
				return v
			}
		}
		return json.RawMessage("null")
	}
	return value.Literal
}

func jsonScalarToText(value json.RawMessage) (string, error) {
	var s string
	if err := json.Unmarshal(value, &s); err == nil {
		return s, nil
	}
	var b bool
	if err := json.Unmarshal(value, &b); err == nil {
		return strconv.FormatBool(b), nil
	}
	var n json.Number
	if err := json.Unmarshal(value, &n); err == nil {
		return n.String(), nil
	}
	return "", fmt.Errorf("policy condition value must be a string, bool, or number, got %s", string(value))
}

func bindScalar(ct colType, value json.RawMessage, params *ParamBuilder) (string, error) {
	switch ct {
	case colUUID:
		var s string
		if err := json.Unmarshal(value, &s); err != nil {
			return "", fmt.Errorf("expected a UUID string, got %s", string(value))
		}
		u, err := uuid.Parse(s)
		if err != nil {
			return "", err
		}
		return params.Push(UUIDBind(u)), nil
	case colTimestamptz:
		text, err := jsonScalarToText(value)
		if err != nil {
			return "", err
		}
		return params.Push(TextBind(text)) + "::timestamptz", nil
	default:
		text, err := jsonScalarToText(value)
		if err != nil {
			return "", err
		}
		return params.Push(TextBind(text)), nil
	}
}

// comparableExprAndValue builds the (lhs, rhs) pair for a gt/gte/lt/lte comparison — ordering
// needs a real numeric cast when comparing against a JSON number (a bare text comparison would
// sort "9" after "10"), so the cast is chosen from the value being compared against. A
// Timestamptz field always casts the bound side to match its own real column type; a Uuid field
// has no meaningful ordering for this use case and is rejected outright.
func comparableExprAndValue(expr string, ct colType, expected json.RawMessage, params *ParamBuilder) (string, string, error) {
	switch ct {
	case colUUID:
		return "", "", fmt.Errorf("gt/gte/lt/lte is not supported on a UUID-typed field")
	case colTimestamptz:
		text, err := jsonScalarToText(expected)
		if err != nil {
			return "", "", err
		}
		return expr, params.Push(TextBind(text)) + "::timestamptz", nil
	default:
		var n json.Number
		if err := json.Unmarshal(expected, &n); err == nil {
			text, err := jsonScalarToText(expected)
			if err != nil {
				return "", "", err
			}
			return "(" + expr + ")::numeric", params.Push(TextBind(text)) + "::numeric", nil
		}
		rhs, err := bindScalar(ct, expected, params)
		if err != nil {
			return "", "", err
		}
		return expr, rhs, nil
	}
}

func ConditionToSQL(condition *permission.PolicyCondition, context *permission.RequestContext, params *ParamBuilder) (string, error) {
	switch {
	case condition.All != nil:
		if len(condition.All) == 0 {
			return "true", nil
		}
		clauses := make([]string, len(condition.All))
		for i := range condition.All {
			c, err := ConditionToSQL(&condition.All[i], context, params)
			if err != nil {
				return "", err
			}
			clauses[i] = c
		}
		return "(" + joinSQL(clauses, " AND ") + ")", nil
	case condition.Any != nil:
		if len(condition.Any) == 0 {
			return "false", nil
		}
		clauses := make([]string, len(condition.Any))
		for i := range condition.Any {
			c, err := ConditionToSQL(&condition.Any[i], context, params)
			if err != nil {
				return "", err
			}
			clauses[i] = c
		}
		return "(" + joinSQL(clauses, " OR ") + ")", nil
	default:
		expr, ct, err := fieldExpression(condition.Attribute, params)
		if err != nil {
			return "", err
		}
		expected := resolveValue(condition.Value, context)

		switch condition.Op {
		case permission.OpEq:
			ph, err := bindScalar(ct, expected, params)
			if err != nil {
				return "", err
			}
			return expr + " = " + ph, nil
		case permission.OpNeq:
			ph, err := bindScalar(ct, expected, params)
			if err != nil {
				return "", err
			}
			return expr + " != " + ph, nil
		case permission.OpIn, permission.OpNotIn:
			var values []json.RawMessage
			_ = json.Unmarshal(expected, &values)
			sqlOp := "IN"
			emptyResult := "false"
			if condition.Op == permission.OpNotIn {
				sqlOp = "NOT IN"
				emptyResult = "true"
			}
			if len(values) == 0 {
				return emptyResult, nil
			}
			phs := make([]string, len(values))
			for i, v := range values {
				ph, err := bindScalar(ct, v, params)
				if err != nil {
					return "", err
				}
				phs[i] = ph
			}
			return expr + " " + sqlOp + " (" + joinSQL(phs, ", ") + ")", nil
		case permission.OpGt, permission.OpGte, permission.OpLt, permission.OpLte:
			lhs, rhs, err := comparableExprAndValue(expr, ct, expected, params)
			if err != nil {
				return "", err
			}
			var sqlOp string
			switch condition.Op {
			case permission.OpGt:
				sqlOp = ">"
			case permission.OpGte:
				sqlOp = ">="
			case permission.OpLt:
				sqlOp = "<"
			default:
				sqlOp = "<="
			}
			return lhs + " " + sqlOp + " " + rhs, nil
		case permission.OpContains, permission.OpNotContains:
			// Contains/NotContains were added for context-subject conditions — those never reach
			// here at all. A record-level policy could still declare one against an array-shaped
			// JSONB field, which this function can't push into SQL: fieldExpression's
			// jsonb_extract_path_text returns the array's own text rendering, not a queryable
			// JSONB value a containment operator could use. Refused loudly rather than silently
			// building an always-false or wrong comparison.
			return "", &UnsupportedContainsConditionInListError{Message: fmt.Sprintf(
				"policy condition attribute %q uses 'contains'/'notContains', not supported in "+
					"list() queries against a record field — only against a context-subject attribute, which "+
					"is evaluated in-process and never reaches SQL generation", condition.Attribute)}
		default:
			return "", fmt.Errorf("unknown condition op %q", condition.Op)
		}
	}
}

func joinSQL(parts []string, sep string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += sep
		}
		out += p
	}
	return out
}

// RecordPolicyWhereClause builds the WHERE fragment for record-level (ABAC) read policies —
// every other permission-decision entry point bypasses policy evaluation for admin, so this
// does too.
//
// Deny-overrides-allow needs its own handling here, separate from EvaluatePolicies — that
// function evaluates one already-fetched record; this one builds a WHERE clause that filters
// many rows in the database without fetching them first, so deny/allow conditions both become
// SQL fragments: (allow1 OR allow2 OR ...) AND NOT (deny1 OR deny2 OR ...).
//
// Returns ("", false) for "no restriction" (admin, or no rows at all).
func RecordPolicyWhereClause(rows []permission.PolicyRow, context *permission.RequestContext, params *ParamBuilder) (string, bool, error) {
	if len(rows) == 0 || context.IsAdmin() {
		return "", false, nil
	}

	var matching []permission.PolicyRow
	for _, row := range rows {
		if permission.RoleGatePassed(row.Roles, row.HasRoles, context.Roles) {
			matching = append(matching, row)
		}
	}

	var allowRows []permission.PolicyRow
	for _, r := range matching {
		if r.Effect == permission.EffectAllow {
			allowRows = append(allowRows, r)
		}
	}
	if len(allowRows) == 0 {
		// Same semantics this always had for "nothing matched": the mere existence of
		// record-level policies for this entity/action switches from "unrestricted" to "must
		// match an allow to be visible" — a deny-only match still grants nothing.
		return "false", true, nil
	}

	allowClauses := make([]string, len(allowRows))
	for i, row := range allowRows {
		if row.Condition != nil {
			c, err := ConditionToSQL(row.Condition, context, params)
			if err != nil {
				return "", false, err
			}
			allowClauses[i] = c
		} else {
			allowClauses[i] = "true"
		}
	}
	allowSQL := "(" + joinSQL(allowClauses, " OR ") + ")"

	var denyRows []permission.PolicyRow
	for _, r := range matching {
		if r.Effect == permission.EffectDeny {
			denyRows = append(denyRows, r)
		}
	}
	if len(denyRows) == 0 {
		return allowSQL, true, nil
	}

	denyClauses := make([]string, len(denyRows))
	for i, row := range denyRows {
		if row.Condition != nil {
			c, err := ConditionToSQL(row.Condition, context, params)
			if err != nil {
				return "", false, err
			}
			denyClauses[i] = c
		} else {
			denyClauses[i] = "true"
		}
	}
	denySQL := "(" + joinSQL(denyClauses, " OR ") + ")"

	return "(" + allowSQL + " AND NOT " + denySQL + ")", true, nil
}
