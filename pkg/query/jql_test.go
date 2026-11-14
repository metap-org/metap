package query

// Ported from the Rust metap-query crate's src/jql.rs test module.

import (
	"testing"

	"github.com/metap-org/metap/pkg/metadata"
	"github.com/stretchr/testify/require"
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
	require.NoError(t, err)
	require.True(t, hasWhere, "unexpected where sql: %q", whereSQL)
	require.NotContains(t, whereSQL, "ILIKE", "unexpected where sql: %q", whereSQL)
	require.False(t, hasOrder, "expected no order")
	// one placeholder: the value (the field is its own column)
	require.Len(t, params.Params, 1, "expected 1 param, got %d", len(params.Params))
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
		require.NoError(t, err, "%q: expected to compile, err=%v hasWhere=%v", c, err, hasWhere)
		require.True(t, hasWhere, "%q: expected to compile, err=%v hasWhere=%v", c, err, hasWhere)
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
		require.NoError(t, err, "%q: expected to compile, err=", c)
		require.True(t, hasWhere, "%q: expected to compile, err=%v", c, err)
	}
}

func TestJqlContainsOperatorWorksOnTextFields(t *testing.T) {
	_, hasWhere, _, _, _, err := jqlCompile(t, `title ~ "kanban"`)
	require.NoError(t, err, "expected to compile, err=")
	require.True(t, hasWhere, "expected to compile, err=%v", err)
}

func TestJqlOrderByCompilesToFieldAndDirection(t *testing.T) {
	_, _, field, desc, hasOrder, err := jqlCompile(t, `priority = "high" ORDER BY storyPoints DESC`)
	require.NoError(t, err)
	require.True(t, hasOrder, "expected order (storyPoints, desc), got (%s, %v, %v)", field, desc, hasOrder)
	require.Equal(t, "storyPoints", field, "expected order (storyPoints, desc), got (%s, %v, %v)", field, desc, hasOrder)
	require.True(t, desc, "expected order (storyPoints, desc), got (%s, %v, %v)", field, desc, hasOrder)
}

func TestJqlOrderByOnlyWithNoFilterExpressionIsAllowed(t *testing.T) {
	_, hasWhere, field, desc, hasOrder, err := jqlCompile(t, `ORDER BY storyPoints ASC`)
	require.NoError(t, err)
	require.False(t, hasWhere, "expected no where clause")
	require.True(t, hasOrder, "expected order (storyPoints, asc), got (%s, %v, %v)", field, desc, hasOrder)
	require.Equal(t, "storyPoints", field, "expected order (storyPoints, asc), got (%s, %v, %v)", field, desc, hasOrder)
	require.False(t, desc, "expected order (storyPoints, asc), got (%s, %v, %v)", field, desc, hasOrder)
}

func TestJqlUnknownFieldIsACleanErrorNotAPanic(t *testing.T) {
	_, _, _, _, _, err := jqlCompile(t, `nope = "x"`)
	require.Error(t, err, "expected an Unknown field error, got %v", err)
	require.Contains(t, err.Error(), "Unknown field", "expected an Unknown field error, got %v", err)
}

func TestJqlRangeOperatorRejectedOnNonOrderableKind(t *testing.T) {
	_, _, _, _, _, err := jqlCompile(t, `priority > "high"`)
	require.Error(t, err, "expected a 'not supported' error, got %v", err)
	require.Contains(t, err.Error(), "not supported", "expected a 'not supported' error, got %v", err)
}

func TestJqlOrderByOnANonSortableFieldIsRejected(t *testing.T) {
	_, _, _, _, _, err := jqlCompile(t, `title = "x" ORDER BY title`)
	require.Error(t, err, "expected a 'not sortable' error, got %v", err)
	require.Contains(t, err.Error(), "not sortable", "expected a 'not sortable' error, got %v", err)
}

func TestJqlUnterminatedStringAndTrailingGarbageAreCleanErrors(t *testing.T) {
	_, _, _, _, _, err := jqlCompile(t, `priority = "high`)
	require.Error(t, err, "expected an error for an unterminated string")
	_, _, _, _, _, err = jqlCompile(t, `priority = "high" wat`)
	require.Error(t, err, "expected an error for trailing garbage")
}
