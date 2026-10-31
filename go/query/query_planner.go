// QueryPlanner — the actual Postgres-dialect seam in this codebase (jsonb_extract_path_text,
// keyset pagination, FTS).
package query

import (
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/metap-org/metap-go/metadata"
	"github.com/metap-org/metap-go/permission"
)

type InvalidCursorError struct{ Message string }

func (e *InvalidCursorError) Error() string { return e.Message }

// UnknownListViewError: ?listView=<name> requested a list view that doesn't exist on this
// entity.
type UnknownListViewError struct{ Name string }

func (e *UnknownListViewError) Error() string { return "Unknown list view: " + e.Name }

type ResolvedSort struct {
	Field      string
	Descending bool
}

// ListInput mirrors the Rust ListInput. Filters is a slice of (field, value) pairs, not a map —
// deterministic iteration order matters here (the generated $N numbering must be reproducible
// for the same input).
type ListInput struct {
	Limit    int64
	Sort     *string
	Filters  [][2]string
	Cursor   *string
	ListView *string
	JQL      *string
}

type PlannedListQuery struct {
	WhereSQL     string
	OrderBySQL   string
	Params       []BindValue
	Limit        int64
	ResolvedSort ResolvedSort
}

func parseSort(candidate *string, sortableFields map[string]bool) *ResolvedSort {
	if candidate == nil {
		return nil
	}
	descending := strings.HasPrefix(*candidate, "-")
	field := strings.TrimPrefix(*candidate, "-")
	if sortableFields[field] {
		return &ResolvedSort{Field: field, Descending: descending}
	}
	return nil
}

type sortColType int

const (
	sortColTimestamptz sortColType = iota
	sortColUUID
	sortColText
)

func (t sortColType) castSuffix() string {
	switch t {
	case sortColTimestamptz:
		return "::timestamptz"
	case sortColUUID:
		return "::uuid"
	default:
		return ""
	}
}

// sortFieldExpression: fieldDef lets a field with a real physical column resolve to that column
// directly instead of the jsonb_extract_path_text(data, ...) fallback every other field uses.
// Only Reference/Id fields get sortColUUID — everything else promoted to a real column is still
// a text-typed column, same cast behavior as the JSONB fallback.
func sortFieldExpression(fieldName string, fieldDef *metadata.EntityField, params *ParamBuilder) (string, sortColType) {
	switch fieldName {
	case "createdAt":
		return "created_at", sortColTimestamptz
	case "updatedAt":
		return "updated_at", sortColTimestamptz
	}
	if fieldDef != nil && metadata.FieldHasRealColumn(*fieldDef) {
		ct := sortColText
		if fieldDef.Kind == metadata.KindReference || fieldDef.Kind == metadata.KindID {
			ct = sortColUUID
		}
		return `"` + fieldDef.Name + `"`, ct
	}
	ph := params.Push(TextBind(fieldName))
	return "jsonb_extract_path_text(data, " + ph + ")", sortColText
}

func bindCursorValue(ct sortColType, value string, params *ParamBuilder) string {
	return params.Push(TextBind(value)) + ct.castSuffix()
}

// cursorCondition builds the keyset-pagination predicate for "every row strictly after the
// cursor's position", correctly accounting for Postgres's own NULL-ordering defaults (NULLS
// LAST for ASC, NULLS FIRST for DESC) instead of assuming every sort value is comparable with
// plain >/</=, which silently drops every row once a NULL sort value is involved, since SQL's
// three-valued logic makes NULL > x/NULL = x "unknown", never true, for any x.
//
// Under that NULL-ordering convention, a NULL sort value behaves as if it were the "largest"
// possible value in both directions — last under ASC, first under DESC. Four cases:
//   - ASC, cursor value real (v): next rows are sort_expr > v, the tiebreak on equal v, OR any
//     NULL row (the trailing NULL block always comes after every real value).
//   - DESC, cursor value real (v): next rows are only smaller reals — the leading NULL block (if
//     any) already sorted before v, already fully consumed by earlier pages.
//   - ASC, cursor value NULL: already inside the trailing NULL block, which is last — nothing but
//     more NULLs (tiebreak on id) can follow.
//   - DESC, cursor value NULL: still inside the leading NULL block — next rows are either more
//     NULLs (tiebreak on id) or, once that block ends, every real value.
func cursorCondition(sortExpr string, descending bool, valuePh *string, idPh string) string {
	switch {
	case !descending && valuePh != nil:
		return fmt.Sprintf("((%s > %s) OR (%s = %s AND id > %s) OR %s IS NULL)", sortExpr, *valuePh, sortExpr, *valuePh, idPh, sortExpr)
	case descending && valuePh != nil:
		return fmt.Sprintf("((%s < %s) OR (%s = %s AND id > %s))", sortExpr, *valuePh, sortExpr, *valuePh, idPh)
	case !descending:
		return fmt.Sprintf("(%s IS NULL AND id > %s)", sortExpr, idPh)
	default:
		return fmt.Sprintf("((%s IS NULL AND id > %s) OR %s IS NOT NULL)", sortExpr, idPh, sortExpr)
	}
}

// PlanList has the same signature shape as the Rust plan_list (metadata + permissions injected,
// not held as struct state).
func PlanList(metadataRegistry *metadata.MetadataRegistry, permissions *permission.PermissionService, entityName string, input ListInput, context *permission.RequestContext, recordReadPolicies []permission.PolicyRow) (*PlannedListQuery, error) {
	entity, ok := metadataRegistry.GetEntity(entityName)
	if !ok {
		return nil, fmt.Errorf("Entity not found: %s", entityName)
	}

	tenantID, err := permissions.ScopedTenant(context)
	if err != nil {
		return nil, err
	}

	var listView *metadata.EntityListView
	if input.ListView != nil {
		for i := range entity.ListViews {
			if entity.ListViews[i].Name == *input.ListView {
				listView = &entity.ListViews[i]
				break
			}
		}
		if listView == nil {
			return nil, &UnknownListViewError{Name: *input.ListView}
		}
	} else if len(entity.ListViews) > 0 {
		listView = &entity.ListViews[0]
	}

	maxLimit := int64(100)
	if listView != nil {
		maxLimit = int64(listView.MaxLimit)
	}
	limit := input.Limit
	if limit > maxLimit {
		limit = maxLimit
	}

	params := NewParamBuilder()
	var conditions []string

	conditions = append(conditions, "tenant_id = "+params.Push(UUIDBind(tenantID)))
	conditions = append(conditions, "deleted = false")

	if recordCondition, ok, err := RecordPolicyWhereClause(recordReadPolicies, context, params); err != nil {
		return nil, err
	} else if ok {
		conditions = append(conditions, recordCondition)
	}

	allowedFilterFields := map[string]bool{}
	if listView != nil {
		for _, f := range listView.Filters {
			allowedFilterFields[f] = true
		}
	}
	fieldsByName := map[string]*metadata.EntityField{}
	for i := range entity.Fields {
		fieldsByName[entity.Fields[i].Name] = &entity.Fields[i]
	}

	for _, kv := range input.Filters {
		field, value := kv[0], kv[1]
		if !allowedFilterFields[field] {
			continue
		}

		fieldDef := fieldsByName[field]
		fieldExpr, fieldColType := sortFieldExpression(field, fieldDef, params)

		isFTS := fieldDef != nil && boolOrFalse(fieldDef.Searchable) && fieldDef.SearchMode != nil && *fieldDef.SearchMode == "fts"
		isSubstringSearch := fieldDef != nil && boolOrFalse(fieldDef.Searchable) && !isFTS

		switch {
		case isFTS:
			ph := params.Push(TextBind(value))
			conditions = append(conditions, "to_tsvector('simple', "+fieldExpr+") @@ plainto_tsquery('simple', "+ph+")")
		case isSubstringSearch:
			ph := params.Push(TextBind(ilikeEscape(value)))
			conditions = append(conditions, fieldExpr+" ILIKE "+ph)
		case value == "":
			// ?field= (empty value) means "field is unset" — the common REST convention. Binding
			// "" as $n::uuid (or any other typed cast) fails outright at the DB with "invalid
			// input syntax". field_expr IS NULL is correct for both storage shapes.
			conditions = append(conditions, fieldExpr+" IS NULL")
		default:
			ph := params.Push(TextBind(value))
			conditions = append(conditions, fieldExpr+" = "+ph+fieldColType.castSuffix())
		}
	}

	var jqlOrderField string
	var jqlOrderDesc, hasJqlOrder bool
	if input.JQL != nil {
		whereSQL, hasWhere, orderField, orderDesc, hasOrder, err := ParseAndCompileJQL(*input.JQL, entity, params)
		if err != nil {
			return nil, err
		}
		if hasWhere {
			conditions = append(conditions, whereSQL)
		}
		jqlOrderField, jqlOrderDesc, hasJqlOrder = orderField, orderDesc, hasOrder
	}

	sortableFields := map[string]bool{"createdAt": true, "updatedAt": true}
	for _, f := range entity.Fields {
		if boolOrFalse(f.Sortable) {
			sortableFields[f.Name] = true
		}
	}

	// JQL's own ORDER BY (already validated sortable) takes priority over the plain ?sort= param
	// when both are present.
	var effectiveSort *string
	if hasJqlOrder {
		s := jqlOrderField
		if jqlOrderDesc {
			s = "-" + s
		}
		effectiveSort = &s
	} else {
		effectiveSort = input.Sort
	}

	resolvedSortPtr := parseSort(effectiveSort, sortableFields)
	if resolvedSortPtr == nil && listView != nil {
		resolvedSortPtr = parseSort(listView.DefaultSort, sortableFields)
	}
	var resolvedSort ResolvedSort
	if resolvedSortPtr != nil {
		resolvedSort = *resolvedSortPtr
	} else {
		resolvedSort = ResolvedSort{Field: "createdAt", Descending: true}
	}

	sortFieldDef := fieldsByName[resolvedSort.Field]
	sortExpr, sortColT := sortFieldExpression(resolvedSort.Field, sortFieldDef, params)

	if input.Cursor != nil {
		cursor, ok := DecodeCursor(*input.Cursor)
		if !ok {
			return nil, &InvalidCursorError{Message: "Cursor does not match the current sort"}
		}

		wantDir := SortAsc
		if resolvedSort.Descending {
			wantDir = SortDesc
		}
		if cursor.Field != resolvedSort.Field || cursor.Dir != wantDir {
			return nil, &InvalidCursorError{Message: "Cursor does not match the current sort"}
		}

		cursorID, err := uuid.Parse(cursor.ID)
		if err != nil {
			return nil, &InvalidCursorError{Message: "Cursor does not match the current sort"}
		}
		var valuePh *string
		if cursor.Value != nil {
			ph := bindCursorValue(sortColT, *cursor.Value, params)
			valuePh = &ph
		}
		idPh := params.Push(UUIDBind(cursorID))

		conditions = append(conditions, cursorCondition(sortExpr, resolvedSort.Descending, valuePh, idPh))
	}

	direction := "ASC"
	nulls := "NULLS LAST"
	if resolvedSort.Descending {
		direction = "DESC"
		nulls = "NULLS FIRST"
	}
	orderBySQL := fmt.Sprintf("%s %s %s, id ASC", sortExpr, direction, nulls)

	return &PlannedListQuery{
		WhereSQL:     strings.Join(conditions, " AND "),
		OrderBySQL:   orderBySQL,
		Params:       params.Params,
		Limit:        limit,
		ResolvedSort: resolvedSort,
	}, nil
}

func boolOrFalse(p *bool) bool { return p != nil && *p }

// ilikeEscape escapes \, %, _ so a substring-search value can't inject SQL wildcards.
func ilikeEscape(value string) string {
	var b strings.Builder
	for _, c := range value {
		if c == '\\' || c == '%' || c == '_' {
			b.WriteByte('\\')
		}
		b.WriteRune(c)
	}
	return "%" + b.String() + "%"
}
