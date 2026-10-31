package query

// Ported from the Rust metap-query crate's src/jql.rs test module.

import (
	"strings"
	"testing"

	"github.com/metap-org/metap-go/metadata"
)

func jqlTestEntity() metadata.EntityDefinition {
	trueVal := true
	return metadata.EntityDefinition{
		Name:      "jira.issues",
		Label:     "Issue",
		TableName: "entities.jira_issues",
		Fields: []metadata.EntityField{
			{Name: "priority", Label: "Priority", Kind: metadata.KindEnum, EnumValues: []string{"low", "high"}, Sortable: &trueVal},
			{Name: "storyPoints", Label: "Story Points", Kind: metadata.KindNumber, Sortable: &trueVal},
			{Name: "title", Label: "Title", Kind: metadata.KindString, Searchable: &trueVal},
		},
	}
}

func jqlCompile(t *testing.T, jql string) (string, bool, string, bool, bool, error) {
	entity := jqlTestEntity()
	params := NewParamBuilder()
	return ParseAndCompileJQL(jql, entity, params)
}

func TestJqlSimpleEqualityCompilesAndBindsOneParam(t *testing.T) {
	entity := jqlTestEntity()
	params := NewParamBuilder()
	whereSQL, hasWhere, _, _, hasOrder, err := ParseAndCompileJQL(`priority = "high"`, entity, params)
	if err != nil {
		t.Fatal(err)
	}
	if !hasWhere || strings.Contains(whereSQL, "ILIKE") {
		t.Fatalf("unexpected where sql: %q", whereSQL)
	}
	if hasOrder {
		t.Fatal("expected no order")
	}
	if len(params.Params) != 2 { // jsonb key placeholder + value placeholder
		t.Fatalf("expected 2 params, got %d", len(params.Params))
	}
}

func TestJqlAndOrNotPrecedenceAndParensAllParse(t *testing.T) {
	cases := []string{
		`priority = "high" AND storyPoints > 3`,
		`priority = "high" OR priority = "low"`,
		`NOT priority = "high"`,
		`(priority = "high" OR priority = "low") AND storyPoints >= 1`,
	}
	for _, c := range cases {
		_, hasWhere, _, _, _, err := jqlCompile(t, c)
		if err != nil || !hasWhere {
			t.Fatalf("%q: expected to compile, err=%v hasWhere=%v", c, err, hasWhere)
		}
	}
}

func TestJqlInAndIsEmptyCompile(t *testing.T) {
	cases := []string{
		`priority IN ("high", "low")`,
		`priority NOT IN ("high")`,
		`storyPoints IS EMPTY`,
		`storyPoints IS NOT EMPTY`,
	}
	for _, c := range cases {
		_, hasWhere, _, _, _, err := jqlCompile(t, c)
		if err != nil || !hasWhere {
			t.Fatalf("%q: expected to compile, err=%v", c, err)
		}
	}
}

func TestJqlContainsOperatorWorksOnTextFields(t *testing.T) {
	_, hasWhere, _, _, _, err := jqlCompile(t, `title ~ "kanban"`)
	if err != nil || !hasWhere {
		t.Fatalf("expected to compile, err=%v", err)
	}
}

func TestJqlOrderByCompilesToFieldAndDirection(t *testing.T) {
	_, _, field, desc, hasOrder, err := jqlCompile(t, `priority = "high" ORDER BY storyPoints DESC`)
	if err != nil {
		t.Fatal(err)
	}
	if !hasOrder || field != "storyPoints" || !desc {
		t.Fatalf("expected order (storyPoints, desc), got (%s, %v, %v)", field, desc, hasOrder)
	}
}

func TestJqlOrderByOnlyWithNoFilterExpressionIsAllowed(t *testing.T) {
	_, hasWhere, field, desc, hasOrder, err := jqlCompile(t, `ORDER BY storyPoints ASC`)
	if err != nil {
		t.Fatal(err)
	}
	if hasWhere {
		t.Fatal("expected no where clause")
	}
	if !hasOrder || field != "storyPoints" || desc {
		t.Fatalf("expected order (storyPoints, asc), got (%s, %v, %v)", field, desc, hasOrder)
	}
}

func TestJqlUnknownFieldIsACleanErrorNotAPanic(t *testing.T) {
	_, _, _, _, _, err := jqlCompile(t, `nope = "x"`)
	if err == nil || !strings.Contains(err.Error(), "Unknown field") {
		t.Fatalf("expected an Unknown field error, got %v", err)
	}
}

func TestJqlRangeOperatorRejectedOnNonOrderableKind(t *testing.T) {
	_, _, _, _, _, err := jqlCompile(t, `priority > "high"`)
	if err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("expected a 'not supported' error, got %v", err)
	}
}

func TestJqlOrderByOnANonSortableFieldIsRejected(t *testing.T) {
	_, _, _, _, _, err := jqlCompile(t, `title = "x" ORDER BY title`)
	if err == nil || !strings.Contains(err.Error(), "not sortable") {
		t.Fatalf("expected a 'not sortable' error, got %v", err)
	}
}

func TestJqlUnterminatedStringAndTrailingGarbageAreCleanErrors(t *testing.T) {
	if _, _, _, _, _, err := jqlCompile(t, `priority = "high`); err == nil {
		t.Fatal("expected an error for an unterminated string")
	}
	if _, _, _, _, _, err := jqlCompile(t, `priority = "high" wat`); err == nil {
		t.Fatal("expected an error for trailing garbage")
	}
}
