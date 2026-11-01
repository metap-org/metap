package metadata

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// implicitSystemFields are always present on every entity's underlying row regardless of
// declared fields.
var implicitSystemFields = map[string]bool{"createdAt": true, "updatedAt": true}

// ValidationError mirrors the Rust MetadataValidationError.
type ValidationError struct {
	Entity string
	Issues []string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("Invalid metadata for entity %q: %s", e.Entity, strings.Join(e.Issues, "; "))
}

// hashShape is the deterministic-hash input shape — label/fields/listViews/workflow, matching
// the Rust HashShape exactly (field order in the struct literal below controls JSON key order
// only if using an ordered encoder; Go's encoding/json already emits object keys in Go struct
// field declaration order for a struct, matching Rust's serde struct order — both sides must
// still produce identical bytes for Hash to be a real content hash, which this file order does).
type hashShape struct {
	Label     string           `json:"label"`
	Fields    []EntityField    `json:"fields"`
	ListViews []EntityListView `json:"listViews"`
	Workflow  *EntityWorkflow  `json:"workflow,omitempty"`
}

// Hash is a deterministic SHA-256 over the entity's shape — label/fields/listViews/workflow.
//
// Divergence from the Rust version, noted rather than hidden: Rust's serde_json::Value stores
// object keys in a BTreeMap (sorted) with no preserve_order feature enabled anywhere in that
// workspace, so its stable_json has every object's keys sorted recursively, independent of
// struct field declaration order. Go's encoding/json has no such global key-sorting pass for
// nested maps it encounters during marshal of a typed struct — map[string]T values ARE sorted
// by Go's encoder, but struct fields are emitted in declaration order, which already matches
// this struct's declared field order here. The two encoders therefore produce byte-identical
// output for this specific shape (no map-typed field exists on EntityField/EntityListView/
// EntityWorkflow themselves — EnumTones/SetFields are the only maps in this whole metadata
// shape and neither is part of hashShape), so the resulting hash values are cross-compatible
// between the Rust and Go ports for the same entity. A future field added as a map type would
// need this re-checked.
func Hash(entity EntityDefinition) (string, error) {
	shape := hashShape{Label: entity.Label, Fields: entity.Fields, ListViews: entity.ListViews, Workflow: entity.Workflow}
	b, err := json.Marshal(shape)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(b)
	return hex.EncodeToString(digest[:]), nil
}

func isSafeFieldName(name string) bool {
	if name == "" {
		return false
	}
	if !isASCIIAlpha(name[0]) {
		return false
	}
	for i := 0; i < len(name); i++ {
		if !isASCIIAlnum(name[i]) && name[i] != '_' {
			return false
		}
	}
	return true
}

func isASCIIAlpha(b byte) bool { return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') }
func isASCIIAlnum(b byte) bool { return isASCIIAlpha(b) || (b >= '0' && b <= '9') }
func isASCIILower(b byte) bool { return b >= 'a' && b <= 'z' }
func isASCIIDigit(b byte) bool { return b >= '0' && b <= '9' }

func isSafeIdentSegment(segment string) bool {
	if segment == "" || !isASCIILower(segment[0]) {
		return false
	}
	for i := 0; i < len(segment); i++ {
		if !isASCIILower(segment[i]) && !isASCIIDigit(segment[i]) && segment[i] != '_' {
			return false
		}
	}
	return true
}

// Validate mirrors the Rust validate() function — boot-time structural validation, driven
// purely by entity metadata, never request input.
func Validate(entity EntityDefinition) error {
	var issues []string
	fieldNames := map[string]bool{}

	for _, field := range entity.Fields {
		if fieldNames[field.Name] {
			issues = append(issues, fmt.Sprintf("duplicate field name %q", field.Name))
		}
		fieldNames[field.Name] = true

		if !isSafeFieldName(field.Name) {
			issues = append(issues, fmt.Sprintf("field name %q must match ^[A-Za-z][A-Za-z0-9_]*$", field.Name))
		}
		if field.Kind == KindEnum && len(field.EnumValues) == 0 {
			issues = append(issues, fmt.Sprintf("field %q is kind \"enum\" but declares no enumValues", field.Name))
		}
		if field.Kind == KindReference && field.RefEntity == nil {
			issues = append(issues, fmt.Sprintf("field %q is kind \"reference\" but declares no refEntity", field.Name))
		}

		isNumeric := field.Kind == KindNumber || field.Kind == KindMoney
		if !isNumeric && (field.Min != nil || field.Max != nil) {
			issues = append(issues, fmt.Sprintf("field %q declares min/max but is kind %q, not number/money", field.Name, field.Kind))
		}
		if field.Min != nil && field.Max != nil && *field.Min > *field.Max {
			issues = append(issues, fmt.Sprintf("field %q has min (%v) greater than max (%v)", field.Name, *field.Min, *field.Max))
		}

		if field.Kind != KindString && (field.MinLength != nil || field.MaxLength != nil) {
			issues = append(issues, fmt.Sprintf("field %q declares minLength/maxLength but is kind %q, not string", field.Name, field.Kind))
		}
		if field.MinLength != nil && field.MaxLength != nil && *field.MinLength > *field.MaxLength {
			issues = append(issues, fmt.Sprintf("field %q has minLength (%v) greater than maxLength (%v)", field.Name, *field.MinLength, *field.MaxLength))
		}
	}

	computedFieldNames := map[string]bool{}
	for _, f := range entity.Fields {
		if f.Computed != nil {
			computedFieldNames[f.Name] = true
		}
	}
	for _, field := range entity.Fields {
		if field.Computed == nil {
			continue
		}
		computed := field.Computed

		if field.Kind != KindString {
			issues = append(issues, fmt.Sprintf(
				"field %q declares computed but is kind %q, not string (v1 only supports string template expressions)",
				field.Name, field.Kind))
		}
		if boolVal(field.Required) {
			issues = append(issues, fmt.Sprintf(
				"field %q declares computed and required — the server always overwrites a computed field's value, required makes no sense here",
				field.Name))
		}
		if boolVal(field.Searchable) || boolVal(field.Sortable) {
			issues = append(issues, fmt.Sprintf(
				"field %q declares computed and searchable/sortable — not supported in v1, there is no materialize step yet (see docs/features/13-computed-derived-field.md)",
				field.Name))
		}

		for _, dep := range computed.DependsOn {
			switch {
			case dep == field.Name:
				issues = append(issues, fmt.Sprintf("field %q has computed.dependsOn referencing itself", field.Name))
			case computedFieldNames[dep]:
				issues = append(issues, fmt.Sprintf(
					"field %q has computed.dependsOn referencing another computed field %q — chaining computed fields is not supported in v1",
					field.Name, dep))
			case !fieldNames[dep]:
				issues = append(issues, fmt.Sprintf("field %q has computed.dependsOn referencing unknown field %q", field.Name, dep))
			}
		}

		dependsOnSet := map[string]bool{}
		for _, d := range computed.DependsOn {
			dependsOnSet[d] = true
		}
		for _, token := range ExpressionTokens(computed.Expression) {
			if !dependsOnSet[token] {
				issues = append(issues, fmt.Sprintf(
					"field %q has computed.expression referencing \"{%s}\" which is not listed in dependsOn", field.Name, token))
			}
		}
	}

	isKnownField := func(name string) bool { return fieldNames[name] || implicitSystemFields[name] }

	for _, listView := range entity.ListViews {
		for _, fieldName := range listView.Fields {
			if !isKnownField(fieldName) {
				issues = append(issues, fmt.Sprintf("listView %q references unknown field %q", listView.Name, fieldName))
			}
		}
		for _, fieldName := range listView.Filters {
			if !isKnownField(fieldName) {
				issues = append(issues, fmt.Sprintf("listView %q filters on unknown field %q", listView.Name, fieldName))
			}
		}
		if listView.DefaultSort != nil {
			sortField := strings.TrimPrefix(*listView.DefaultSort, "-")
			if !isKnownField(sortField) {
				issues = append(issues, fmt.Sprintf("listView %q defaultSort references unknown field %q", listView.Name, sortField))
			}
		}
	}

	if workflow := entity.Workflow; workflow != nil {
		if !fieldNames[workflow.StateField] {
			issues = append(issues, fmt.Sprintf("workflow.stateField %q is not a declared field", workflow.StateField))
		}
		if workflow.InitialState == "" {
			issues = append(issues, "workflow.initialState must be a non-empty string")
		}
		for _, state := range workflow.TerminalStates {
			if state == "" {
				issues = append(issues, "workflow.terminalStates contains an empty string")
			}
		}

		seenTransitionKeys := map[string]bool{}
		for _, transition := range workflow.Transitions {
			if transition.From == "" || transition.To == "" || transition.Action == "" {
				issues = append(issues, fmt.Sprintf(
					"workflow transition is missing from/to/action: {\"from\":%q,\"to\":%q,\"action\":%q}",
					transition.From, transition.To, transition.Action))
				continue
			}
			key := transition.From + "::" + transition.Action
			if seenTransitionKeys[key] {
				issues = append(issues, fmt.Sprintf("duplicate transition action %q from state %q", transition.Action, transition.From))
			}
			seenTransitionKeys[key] = true

			for fieldName := range transition.SetFields {
				if fieldName == workflow.StateField {
					issues = append(issues, fmt.Sprintf(
						"transition %q setFields sets %q, which is stateField — use `to` instead", transition.Action, fieldName))
				} else if !isKnownField(fieldName) {
					issues = append(issues, fmt.Sprintf("transition %q setFields references unknown field %q", transition.Action, fieldName))
				}
			}
		}

		var stateEnumValues []string
		for _, f := range entity.Fields {
			if f.Name == workflow.StateField && f.Kind == KindEnum {
				stateEnumValues = f.EnumValues
				break
			}
		}
		if stateEnumValues != nil {
			enumSet := map[string]bool{}
			for _, v := range stateEnumValues {
				enumSet[v] = true
			}
			if workflow.InitialState != "" && !enumSet[workflow.InitialState] {
				issues = append(issues, fmt.Sprintf(
					"workflow.initialState %q is not one of stateField %q's enumValues", workflow.InitialState, workflow.StateField))
			}
			for _, state := range workflow.TerminalStates {
				if state != "" && !enumSet[state] {
					issues = append(issues, fmt.Sprintf(
						"workflow.terminalStates contains %q, not one of stateField %q's enumValues", state, workflow.StateField))
				}
			}
			for _, transition := range workflow.Transitions {
				if transition.From == "" || transition.To == "" {
					continue
				}
				if !enumSet[transition.From] {
					issues = append(issues, fmt.Sprintf(
						"workflow transition %q has from-state %q, not one of stateField %q's enumValues",
						transition.Action, transition.From, workflow.StateField))
				}
				if !enumSet[transition.To] {
					issues = append(issues, fmt.Sprintf(
						"workflow transition %q has to-state %q, not one of stateField %q's enumValues",
						transition.Action, transition.To, workflow.StateField))
				}
			}
		}
	}

	tableNameSegments := strings.Split(entity.TableName, ".")
	tableNameOK := strings.Contains(entity.TableName, ".")
	for _, seg := range tableNameSegments {
		if !isSafeIdentSegment(seg) {
			tableNameOK = false
		}
	}
	if !tableNameOK {
		issues = append(issues, fmt.Sprintf(
			"tableName %q must be a schema-qualified ^[a-z][a-z0-9_]*\\.[a-z][a-z0-9_]*$ name", entity.TableName))
	}

	seenFieldSets := map[string]bool{}
	for _, constraint := range entity.UniqueConstraints {
		if len(constraint.Fields) < 2 {
			issues = append(issues, fmt.Sprintf(
				"uniqueConstraints entry %v needs >= 2 fields — a single field belongs on that field's own \"unique\" flag instead", constraint.Fields))
			continue
		}
		namesInConstraint := map[string]bool{}
		for _, name := range constraint.Fields {
			if !fieldNames[name] {
				issues = append(issues, fmt.Sprintf("uniqueConstraints entry %v references unknown field %q", constraint.Fields, name))
			}
			if namesInConstraint[name] {
				issues = append(issues, fmt.Sprintf("uniqueConstraints entry %v lists field %q more than once", constraint.Fields, name))
			}
			namesInConstraint[name] = true
		}
		sorted := append([]string{}, constraint.Fields...)
		sort.Strings(sorted)
		key := strings.Join(sorted, "\x00")
		if seenFieldSets[key] {
			issues = append(issues, fmt.Sprintf("uniqueConstraints has more than one entry over the same field set %v", constraint.Fields))
		}
		seenFieldSets[key] = true
	}

	if audit := entity.Audit; audit != nil {
		seen := map[string]bool{}
		for _, name := range audit.RedactedFields {
			if !fieldNames[name] {
				issues = append(issues, fmt.Sprintf("audit.redactedFields references unknown field %q", name))
			}
			if seen[name] {
				issues = append(issues, fmt.Sprintf("audit.redactedFields lists field %q more than once", name))
			}
			seen[name] = true
		}
	}

	entityNameOK := strings.Contains(entity.Name, ".")
	for _, seg := range strings.Split(entity.Name, ".") {
		if !isSafeIdentSegment(seg) {
			entityNameOK = false
		}
	}
	if !entityNameOK {
		issues = append(issues, fmt.Sprintf("entity name %q must be a dotted ^[a-z][a-z0-9_]*(\\.[a-z][a-z0-9_]*)+$ name", entity.Name))
	}

	if len(issues) == 0 {
		return nil
	}
	return &ValidationError{Entity: entity.Name, Issues: issues}
}
