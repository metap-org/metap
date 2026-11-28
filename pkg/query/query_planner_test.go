package query

// Pure unit tests for PlanList's planning logic (no DB — it only builds SQL text).

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/metap-org/metap/pkg/metadata"
	"github.com/metap-org/metap/pkg/permission"
	"github.com/stretchr/testify/require"
)

func qpTestRegistry(t *testing.T, entity metadata.EntityDefinition) *metadata.MetadataRegistry {
	reg := metadata.NewMetadataRegistry()
	require.NoError(t, reg.Register(entity))
	return reg
}

func qpTestContext(tenantID uuid.UUID) *permission.RequestContext {
	return &permission.RequestContext{TenantID: tenantID.String()}
}

func qpTestEntity() metadata.EntityDefinition {
	trueVal := true
	return metadata.EntityDefinition{
		Name:      "test.widgets",
		Label:     "Widget",
		TableName: "entities.test_widgets",
		Fields: []metadata.EntityField{
			{Name: "status", Label: "Status", Kind: metadata.KindString, Sortable: &trueVal},
			{Name: "sku", Label: "SKU", Kind: metadata.KindString, Searchable: &trueVal},
		},
		ListViews: []metadata.EntityListView{
			{Name: "default", Label: "Default", Filters: []string{"status", "sku"}, MaxLimit: 50},
		},
	}
}

func TestPlanListRejectsAnUnknownEntity(t *testing.T) {
	reg := metadata.NewMetadataRegistry()
	perms := permission.NewPermissionService(nil)
	_, err := PlanList(reg, perms, "no.such.entity", ListInput{}, qpTestContext(uuid.New()), nil)
	require.Error(t, err, "expected an error for an unknown entity")
}

func TestPlanListScopesByTenantAndExcludesDeleted(t *testing.T) {
	reg := qpTestRegistry(t, qpTestEntity())
	perms := permission.NewPermissionService(nil)
	tenantID := uuid.New()
	q, err := PlanList(reg, perms, "test.widgets", ListInput{}, qpTestContext(tenantID), nil)
	require.NoError(t, err)
	require.Contains(t, q.WhereSQL, "tenant_id = $1", "expected tenant scoping, got %q", q.WhereSQL)
	require.Contains(t, q.WhereSQL, "deleted = false", "expected deleted exclusion, got %q", q.WhereSQL)
	require.NotEqual(t, 0, len(q.Params), "expected the first param to be the tenant id, bound as a native UUID")
	require.True(t, q.Params[0].IsUUID(), "expected the first param to be the tenant id, bound as a native UUID")
}

func TestPlanListRejectsAnUnknownListView(t *testing.T) {
	reg := qpTestRegistry(t, qpTestEntity())
	perms := permission.NewPermissionService(nil)
	listView := "nope"
	_, err := PlanList(reg, perms, "test.widgets", ListInput{ListView: &listView}, qpTestContext(uuid.New()), nil)
	var target *UnknownListViewError
	require.Error(t, err, "expected an error")
	_, ok := err.(*UnknownListViewError)
	require.True(t, ok, "expected %T, got %T: %v", target, err, err)
}

// A filter the list view does not declare is refused. Dropping it would answer with more rows than
// were asked for, and code that filters to find "this tenant's" or "this domain's" records would
// act on everybody's.
func TestPlanListRefusesAFilterNotInTheListViewsFilters(t *testing.T) {
	reg := qpTestRegistry(t, qpTestEntity())
	perms := permission.NewPermissionService(nil)
	for _, filter := range [][2]string{{"notAllowed", "x"}, {"notAllowed", ""}} {
		_, err := PlanList(reg, perms, "test.widgets", ListInput{Filters: [][2]string{filter}}, qpTestContext(uuid.New()), nil)
		var unknown *UnknownFilterError
		require.ErrorAs(t, err, &unknown, "%v", filter)
		require.Equal(t, filter[0], unknown.Field)
	}
}

func TestPlanListUsesIlikeForASearchableFieldFilter(t *testing.T) {
	reg := qpTestRegistry(t, qpTestEntity())
	perms := permission.NewPermissionService(nil)
	q, err := PlanList(reg, perms, "test.widgets", ListInput{Filters: [][2]string{{"sku", "wid"}}}, qpTestContext(uuid.New()), nil)
	require.NoError(t, err)
	require.Contains(t, q.WhereSQL, "ILIKE", "expected an ILIKE clause, got %q", q.WhereSQL)
}

func TestPlanListEmptyFilterValueMeansFieldIsNull(t *testing.T) {
	reg := qpTestRegistry(t, qpTestEntity())
	perms := permission.NewPermissionService(nil)
	q, err := PlanList(reg, perms, "test.widgets", ListInput{Filters: [][2]string{{"status", ""}}}, qpTestContext(uuid.New()), nil)
	require.NoError(t, err)
	require.Contains(t, q.WhereSQL, "IS NULL", "expected an IS NULL clause, got %q", q.WhereSQL)
}

func TestPlanListDefaultsSortToCreatedAtDescending(t *testing.T) {
	reg := qpTestRegistry(t, qpTestEntity())
	perms := permission.NewPermissionService(nil)
	q, err := PlanList(reg, perms, "test.widgets", ListInput{}, qpTestContext(uuid.New()), nil)
	require.NoError(t, err)
	require.Equal(t, "createdAt", q.ResolvedSort.Field, "expected default sort createdAt desc, got %+v", q.ResolvedSort)
	require.True(t, q.ResolvedSort.Descending, "expected default sort createdAt desc, got %+v", q.ResolvedSort)
	require.Contains(t, q.OrderBySQL, "NULLS FIRST", "expected explicit NULLS FIRST for a descending sort, got %q", q.OrderBySQL)
}

func TestPlanListRejectsAMalformedCursor(t *testing.T) {
	reg := qpTestRegistry(t, qpTestEntity())
	perms := permission.NewPermissionService(nil)
	bad := "not-a-valid-cursor"
	_, err := PlanList(reg, perms, "test.widgets", ListInput{Cursor: &bad}, qpTestContext(uuid.New()), nil)
	_, ok := err.(*InvalidCursorError)
	require.True(t, ok, "expected an InvalidCursorError, got %T: %v", err, err)
}

func TestPlanListRejectsACursorWhoseSortDoesntMatch(t *testing.T) {
	reg := qpTestRegistry(t, qpTestEntity())
	perms := permission.NewPermissionService(nil)
	cursorStr := EncodeCursor(Cursor{Field: "status", Value: strPtr("open"), ID: uuid.New().String(), Dir: SortAsc})
	// Default resolved sort is createdAt desc, not status asc — must be rejected.
	_, err := PlanList(reg, perms, "test.widgets", ListInput{Cursor: &cursorStr}, qpTestContext(uuid.New()), nil)
	_, ok := err.(*InvalidCursorError)
	require.True(t, ok, "expected an InvalidCursorError, got %T: %v", err, err)
}

func TestPlanListAppliesJqlWhereAndOrderBy(t *testing.T) {
	reg := qpTestRegistry(t, qpTestEntity())
	perms := permission.NewPermissionService(nil)
	jql := `status = "open" ORDER BY status DESC`
	q, err := PlanList(reg, perms, "test.widgets", ListInput{JQL: &jql}, qpTestContext(uuid.New()), nil)
	require.NoError(t, err)
	// The JQL field name is bound as a parameter, not inlined into the SQL text (same
	// jsonb_extract_path_text(data, $n) shape every other field path uses) — so check for the
	// extra AND'd condition rather than a literal "status" substring.
	require.False(t, strings.Count(q.WhereSQL, " AND ") < 2, "expected the JQL where fragment folded in as a 3rd condition, got %q", q.WhereSQL)
	require.Equal(t, "status", q.ResolvedSort.Field, "expected JQL's ORDER BY to win, got %+v", q.ResolvedSort)
	require.True(t, q.ResolvedSort.Descending, "expected JQL's ORDER BY to win, got %+v", q.ResolvedSort)
}
