package workflow

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/metap-org/metap/pkg/metadata"
	"github.com/metap-org/metap/pkg/permission"
)

// Explain turns a failed guard or validator into what a client shows and what it can render
// itself: the message (a declared one as is; otherwise a sentence built from the field's label,
// e.g. `Verification Status must be "verified" (currently empty)`), and the failing check as data
// (`field`, `label`, `op`, `expected`, `actual`) for a frontend that localizes it. Anything that is
// not a ConditionError comes back as its Error() with no details.
func Explain(err error, entity *metadata.EntityDefinition) (string, map[string]any) {
	var ce *ConditionError
	if !errors.As(err, &ce) {
		return err.Error(), nil
	}
	if ce.Failure == nil {
		return ce.Message, nil
	}
	f := ce.Failure
	label := fieldLabel(entity, f.Attribute)
	details := map[string]any{
		"field": f.Attribute, "label": label, "op": string(f.Op),
		"expected": rawOrNull(f.Expected), "actual": rawOrNull(f.Actual),
	}
	if ce.Declared {
		return ce.Message, details
	}
	return label + " " + describeOp(f.Op) + " " + describeValue(f.Expected) +
		" (currently " + describeValue(f.Actual) + ")", details
}

func fieldLabel(entity *metadata.EntityDefinition, attribute string) string {
	if entity != nil {
		for _, field := range entity.Fields {
			if field.Name == attribute && field.Label != "" {
				return field.Label
			}
		}
	}
	return attribute
}

var opWords = map[permission.ConditionOp]string{
	permission.OpEq: "must be", permission.OpNeq: "must not be",
	permission.OpIn: "must be one of", permission.OpNotIn: "must not be one of",
	permission.OpGt: "must be greater than", permission.OpGte: "must be at least",
	permission.OpLt: "must be less than", permission.OpLte: "must be at most",
	permission.OpContains: "must contain", permission.OpNotContains: "must not contain",
}

func describeOp(op permission.ConditionOp) string {
	if w, ok := opWords[op]; ok {
		return w
	}
	return string(op)
}

// describeValue: JSON as written, except that an absent or null value reads "empty".
func describeValue(raw json.RawMessage) string {
	text := strings.TrimSpace(string(raw))
	if text == "" || text == "null" {
		return "empty"
	}
	return text
}

func rawOrNull(raw json.RawMessage) json.RawMessage {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return json.RawMessage("null")
	}
	return raw
}
