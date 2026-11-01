package metadata

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/metap-org/metap-go/permission"
)

func f(name string, kind FieldKind) EntityField {
	return EntityField{Name: name, Label: name, Kind: kind}
}

func ent(name string, tableName string, fields []EntityField) EntityDefinition {
	return EntityDefinition{Name: name, Label: name, TableName: tableName, Fields: fields}
}

func validEntity(fields ...EntityField) EntityDefinition {
	return ent("crm.customers", "entities.crm_customers", fields)
}

func u32Ptr(v uint32) *uint32   { return &v }
func f64Ptr(v float64) *float64 { return &v }

func mustHaveIssueContaining(t *testing.T, err error, substr string) {
	t.Helper()
	ve, ok := err.(*ValidationError)
	if !ok {
		t.Fatalf("expected *ValidationError, got %T (%v)", err, err)
	}
	for _, issue := range ve.Issues {
		if strings.Contains(issue, substr) {
			return
		}
	}
	t.Fatalf("expected an issue containing %q, got %v", substr, ve.Issues)
}

func TestValidEntityPasses(t *testing.T) {
	if err := Validate(validEntity(f("name", KindString))); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestGenericRecordsTableIsNoLongerAValidTableName(t *testing.T) {
	e := ent("crm.customers", "records", nil)
	if err := Validate(e); err == nil {
		t.Fatal("expected rejection")
	}
}

func TestABareTableNameWithNoSchemaIsRejected(t *testing.T) {
	e := ent("crm.customers", "crm_customers", nil)
	if err := Validate(e); err == nil {
		t.Fatal("expected rejection")
	}
}

func TestDuplicateFieldNameIsRejected(t *testing.T) {
	err := Validate(validEntity(f("name", KindString), f("name", KindNumber)))
	mustHaveIssueContaining(t, err, "duplicate field name")
}

func TestFieldNameWithUnsafeCharactersIsRejected(t *testing.T) {
	err := Validate(validEntity(f("bad-name", KindString)))
	mustHaveIssueContaining(t, err, "must match")
}

func TestFieldNameWithUnderscoreAndDigitsIsAllowed(t *testing.T) {
	if err := Validate(validEntity(f("field_2", KindString))); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEnumWithoutValuesIsRejected(t *testing.T) {
	err := Validate(validEntity(f("status", KindEnum)))
	mustHaveIssueContaining(t, err, "declares no enumValues")
}

func TestMinMaxOnANonNumericFieldIsRejected(t *testing.T) {
	field := f("name", KindString)
	field.Min = f64Ptr(1)
	err := Validate(validEntity(field))
	mustHaveIssueContaining(t, err, "declares min/max")
}

func TestMinGreaterThanMaxIsRejected(t *testing.T) {
	field := f("amount", KindNumber)
	field.Min = f64Ptr(10)
	field.Max = f64Ptr(5)
	err := Validate(validEntity(field))
	mustHaveIssueContaining(t, err, "greater than max")
}

func TestMinMaxOnANumericFieldPasses(t *testing.T) {
	field := f("amount", KindNumber)
	field.Min = f64Ptr(1)
	field.Max = f64Ptr(10)
	if err := Validate(validEntity(field)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMinLengthMaxLengthOnANonStringFieldIsRejected(t *testing.T) {
	field := f("amount", KindNumber)
	field.MinLength = u32Ptr(1)
	err := Validate(validEntity(field))
	mustHaveIssueContaining(t, err, "declares minLength/maxLength")
}

func TestMinLengthGreaterThanMaxLengthIsRejected(t *testing.T) {
	field := f("name", KindString)
	field.MinLength = u32Ptr(10)
	field.MaxLength = u32Ptr(5)
	err := Validate(validEntity(field))
	mustHaveIssueContaining(t, err, "greater than maxLength")
}

func TestListViewUnknownFieldIsRejected(t *testing.T) {
	e := validEntity(f("name", KindString))
	e.ListViews = []EntityListView{{Name: "all", Label: "All", Fields: []string{"ghost"}, Filters: []string{}, MaxLimit: 50}}
	err := Validate(e)
	mustHaveIssueContaining(t, err, "unknown field")
}

func TestListViewImplicitSystemFieldIsAllowed(t *testing.T) {
	e := validEntity(f("name", KindString))
	e.ListViews = []EntityListView{{Name: "all", Label: "All", Fields: []string{"name", "createdAt", "updatedAt"}, Filters: []string{}, MaxLimit: 50}}
	if err := Validate(e); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestHashIsDeterministicAndOrderIndependentAcrossCalls(t *testing.T) {
	e := validEntity(f("name", KindString))
	h1, err1 := Hash(e)
	h2, err2 := Hash(e)
	if err1 != nil || err2 != nil {
		t.Fatalf("errors: %v %v", err1, err2)
	}
	if h1 != h2 {
		t.Fatalf("hash not deterministic: %s != %s", h1, h2)
	}
}

func TestHashChangesWhenShapeChanges(t *testing.T) {
	e1 := validEntity(f("name", KindString))
	e2 := validEntity(f("name", KindString), f("age", KindNumber))
	h1, _ := Hash(e1)
	h2, _ := Hash(e2)
	if h1 == h2 {
		t.Fatal("expected hash to change when shape changes")
	}
}

func statusField(values ...string) EntityField {
	field := f("status", KindEnum)
	field.EnumValues = values
	return field
}

func TestTransitionToStateNotInEnumValuesIsRejected(t *testing.T) {
	e := validEntity(statusField("open", "closed"))
	e.Workflow = &EntityWorkflow{
		StateField: "status", InitialState: "open", TerminalStates: []string{"closed"},
		Transitions: []WorkflowTransition{{Action: "close", From: "open", To: "archived", Label: "Close"}},
	}
	err := Validate(e)
	mustHaveIssueContaining(t, err, "to-state")
}

func TestWorkflowInitialStateNotInEnumValuesIsRejected(t *testing.T) {
	e := validEntity(statusField("open", "closed"))
	e.Workflow = &EntityWorkflow{StateField: "status", InitialState: "unknown", TerminalStates: []string{"closed"}}
	err := Validate(e)
	mustHaveIssueContaining(t, err, "initialState")
}

func TestWorkflowStatesMatchingEnumValuesPass(t *testing.T) {
	e := validEntity(statusField("open", "closed"))
	e.Workflow = &EntityWorkflow{
		StateField: "status", InitialState: "open", TerminalStates: []string{"closed"},
		Transitions: []WorkflowTransition{{Action: "close", From: "open", To: "closed", Label: "Close"}},
	}
	if err := Validate(e); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestHashChangesWhenOnlyATransitionGuardChanges(t *testing.T) {
	e1 := validEntity(statusField("open", "closed"))
	e1.Workflow = &EntityWorkflow{
		StateField: "status", InitialState: "open", TerminalStates: []string{"closed"},
		Transitions: []WorkflowTransition{{Action: "close", From: "open", To: "closed", Label: "Close"}},
	}
	e2 := validEntity(statusField("open", "closed"))
	guard := permission.AttributeCondition("status", permission.OpEq, permission.LiteralValue(json.RawMessage(`"open"`)))
	e2.Workflow = &EntityWorkflow{
		StateField: "status", InitialState: "open", TerminalStates: []string{"closed"},
		Transitions: []WorkflowTransition{{
			Action: "close", From: "open", To: "closed", Label: "Close",
			Guard: &guard,
		}},
	}
	h1, _ := Hash(e1)
	h2, _ := Hash(e2)
	if h1 == h2 {
		t.Fatal("expected hash to change when a transition's guard changes")
	}
}

func TestValidComputedFieldPasses(t *testing.T) {
	full := f("fullName", KindString)
	full.Computed = &ComputedSpec{Expression: "{firstName} {lastName}", DependsOn: []string{"firstName", "lastName"}}
	if err := Validate(validEntity(f("firstName", KindString), f("lastName", KindString), full)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestComputedFieldMustBeStringKind(t *testing.T) {
	field := f("total", KindNumber)
	field.Computed = &ComputedSpec{Expression: "", DependsOn: []string{}}
	err := Validate(validEntity(field))
	mustHaveIssueContaining(t, err, "not string")
}

func TestComputedFieldCannotBeRequired(t *testing.T) {
	field := f("fullName", KindString)
	field.Required = boolPtr(true)
	field.Computed = &ComputedSpec{Expression: "", DependsOn: []string{}}
	err := Validate(validEntity(field))
	mustHaveIssueContaining(t, err, "required")
}

func TestComputedFieldCannotBeSearchableOrSortable(t *testing.T) {
	field := f("fullName", KindString)
	field.Searchable = boolPtr(true)
	field.Computed = &ComputedSpec{Expression: "", DependsOn: []string{}}
	err := Validate(validEntity(field))
	mustHaveIssueContaining(t, err, "searchable/sortable")
}

func TestComputedFieldDependsOnUnknownFieldIsRejected(t *testing.T) {
	field := f("fullName", KindString)
	field.Computed = &ComputedSpec{Expression: "{ghost}", DependsOn: []string{"ghost"}}
	err := Validate(validEntity(field))
	mustHaveIssueContaining(t, err, "unknown field")
}

func TestComputedFieldDependsOnItselfIsRejected(t *testing.T) {
	field := f("fullName", KindString)
	field.Computed = &ComputedSpec{Expression: "{fullName}", DependsOn: []string{"fullName"}}
	err := Validate(validEntity(field))
	mustHaveIssueContaining(t, err, "referencing itself")
}

func TestComputedFieldDependsOnAnotherComputedFieldIsRejected(t *testing.T) {
	base := f("base", KindString)
	base.Computed = &ComputedSpec{Expression: "x", DependsOn: []string{}}
	chained := f("chained", KindString)
	chained.Computed = &ComputedSpec{Expression: "{base}", DependsOn: []string{"base"}}
	err := Validate(validEntity(base, chained))
	mustHaveIssueContaining(t, err, "chaining computed fields")
}

func TestComputedFieldExpressionTokenMissingFromDependsOnIsRejected(t *testing.T) {
	field := f("fullName", KindString)
	field.Computed = &ComputedSpec{Expression: "{firstName}", DependsOn: []string{}}
	err := Validate(validEntity(f("firstName", KindString), field))
	mustHaveIssueContaining(t, err, "not listed in dependsOn")
}

func TestCompositeUniqueConstraintOverExistingFieldsPasses(t *testing.T) {
	e := validEntity(f("type", KindString), f("value", KindString))
	e.UniqueConstraints = []EntityUniqueConstraint{{Fields: []string{"type", "value"}}}
	if err := Validate(e); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCompositeUniqueConstraintWithOneFieldIsRejected(t *testing.T) {
	e := validEntity(f("type", KindString))
	e.UniqueConstraints = []EntityUniqueConstraint{{Fields: []string{"type"}}}
	err := Validate(e)
	mustHaveIssueContaining(t, err, ">= 2 fields")
}

func TestCompositeUniqueConstraintReferencingUnknownFieldIsRejected(t *testing.T) {
	e := validEntity(f("type", KindString), f("value", KindString))
	e.UniqueConstraints = []EntityUniqueConstraint{{Fields: []string{"type", "ghost"}}}
	err := Validate(e)
	mustHaveIssueContaining(t, err, "unknown field")
}

func TestCompositeUniqueConstraintWithARepeatedFieldIsRejected(t *testing.T) {
	e := validEntity(f("type", KindString), f("value", KindString))
	e.UniqueConstraints = []EntityUniqueConstraint{{Fields: []string{"type", "type"}}}
	err := Validate(e)
	mustHaveIssueContaining(t, err, "more than once")
}

func TestDuplicateCompositeUniqueConstraintFieldSetIsRejectedRegardlessOfOrder(t *testing.T) {
	e := validEntity(f("type", KindString), f("value", KindString))
	e.UniqueConstraints = []EntityUniqueConstraint{
		{Fields: []string{"type", "value"}},
		{Fields: []string{"value", "type"}},
	}
	err := Validate(e)
	mustHaveIssueContaining(t, err, "same field set")
}

func TestAuditRedactedFieldsOverExistingFieldsPasses(t *testing.T) {
	e := validEntity(f("secret", KindString))
	e.Audit = &EntityAuditConfig{Enabled: true, RedactedFields: []string{"secret"}}
	if err := Validate(e); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAuditRedactedFieldsReferencingAnUnknownFieldIsRejected(t *testing.T) {
	e := validEntity(f("name", KindString))
	e.Audit = &EntityAuditConfig{Enabled: true, RedactedFields: []string{"ghost"}}
	err := Validate(e)
	mustHaveIssueContaining(t, err, "unknown field")
}

func TestAuditRedactedFieldsListingTheSameFieldTwiceIsRejected(t *testing.T) {
	e := validEntity(f("secret", KindString))
	e.Audit = &EntityAuditConfig{Enabled: true, RedactedFields: []string{"secret", "secret"}}
	err := Validate(e)
	mustHaveIssueContaining(t, err, "more than once")
}
