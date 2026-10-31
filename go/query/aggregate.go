// Aggregation planner — the GROUP BY/COUNT/SUM counterpart to PlanList. Same boundaries as
// PlanList, deliberately: tenant scope always applied, record-level read policies (ABAC) applied
// to the rows before they're aggregated, every field named by the caller validated against
// entity metadata.
package query

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/metap-org/metap-go/metadata"
	"github.com/metap-org/metap-go/permission"
)

// InvalidAggregateError: caller asked for something this planner refuses to turn into SQL — an
// unknown field, a non-numeric field under sum, a group-by on a field nobody indexed.
type InvalidAggregateError struct{ Message string }

func (e *InvalidAggregateError) Error() string { return e.Message }

func invalidAggregate(format string, args ...any) error {
	return &InvalidAggregateError{Message: fmt.Sprintf(format, args...)}
}

// AggregateFn: the five aggregate functions this planner will emit — a closed set, never a
// caller-supplied string.
type AggregateFn int

const (
	AggCount AggregateFn = iota
	AggSum
	AggAvg
	AggMin
	AggMax
)

func (f AggregateFn) sqlName() string {
	switch f {
	case AggCount:
		return "count"
	case AggSum:
		return "sum"
	case AggAvg:
		return "avg"
	case AggMin:
		return "min"
	default:
		return "max"
	}
}

// AggregateMetric is one requested metric. Field is nil only for Count (count(*)); every other
// function needs a field, and that field must be numeric — enforced in PlanAggregate.
type AggregateMetric struct {
	Func  AggregateFn
	Field *string
}

// ParseAggregateMetric parses the wire form: "count", or "<fn>:<field>" (e.g.
// "sum:bytesBlocked").
func ParseAggregateMetric(raw string) (AggregateMetric, error) {
	funcRaw, field, hasField := raw, "", false
	if idx := strings.IndexByte(raw, ':'); idx >= 0 {
		funcRaw, field, hasField = raw[:idx], strings.TrimSpace(raw[idx+1:]), true
	}
	var fn AggregateFn
	switch strings.TrimSpace(funcRaw) {
	case "count":
		fn = AggCount
	case "sum":
		fn = AggSum
	case "avg":
		fn = AggAvg
	case "min":
		fn = AggMin
	case "max":
		fn = AggMax
	default:
		return AggregateMetric{}, invalidAggregate("Unknown aggregate function: %s", funcRaw)
	}
	if fn != AggCount && !hasField {
		return AggregateMetric{}, invalidAggregate("`%s` needs a field — write it as `%s:<field>`.", fn.sqlName(), fn.sqlName())
	}
	var fieldPtr *string
	if hasField {
		fieldPtr = &field
	}
	return AggregateMetric{Func: fn, Field: fieldPtr}, nil
}

// Alias is the key this metric appears under in each result row. count stays count; everything
// else is <fn>_<field> (sum_bytesBlocked) so two metrics over different fields can't collide.
func (m AggregateMetric) Alias() string {
	if m.Field != nil {
		return m.Func.sqlName() + "_" + *m.Field
	}
	return m.Func.sqlName()
}

// TimeBucket: time granularity for a time-series aggregation — maps 1:1 onto date_trunc's own
// units.
type TimeBucket int

const (
	BucketHour TimeBucket = iota
	BucketDay
	BucketWeek
	BucketMonth
)

func ParseTimeBucket(raw string) (TimeBucket, error) {
	switch strings.TrimSpace(raw) {
	case "hour":
		return BucketHour, nil
	case "day":
		return BucketDay, nil
	case "week":
		return BucketWeek, nil
	case "month":
		return BucketMonth, nil
	default:
		return 0, invalidAggregate("Unknown time bucket: %s (expected hour/day/week/month)", raw)
	}
}

func (b TimeBucket) truncUnit() string {
	switch b {
	case BucketHour:
		return "hour"
	case BucketDay:
		return "day"
	case BucketWeek:
		return "week"
	default:
		return "month"
	}
}

type AggregateInput struct {
	Metrics   []AggregateMetric // at least one — an aggregation with no metric has no answer to give
	GroupBy   *string
	Bucket    *TimeBucket
	TimeField *string
	Filters   [][2]string
	Since     *string
	Until     *string
	Limit     int64
}

// MaxGroups is the hard ceiling on returned groups regardless of what the caller asks for.
const MaxGroups int64 = 500
const DefaultGroups int64 = 100
const maxMetrics = 10

// AggregateSpec is the raw wire shape of an aggregate request — every metric/bucket is still a
// string here, not yet parsed. One shape for every transport (REST JSON body, gRPC Struct,
// GraphQL Json scalar) — IntoInput is the single parse step every one of them calls.
type AggregateSpec struct {
	Metrics   []string
	GroupBy   *string
	Bucket    *string
	TimeField *string
	Filters   map[string]string
	Since     *string
	Until     *string
	Limit     *int64
}

// IntoInput parses metric/bucket strings, same defaults as before this type existed (no metric
// -> a bare count; no limit -> DefaultGroups).
func (s AggregateSpec) IntoInput() (AggregateInput, error) {
	var metrics []AggregateMetric
	if len(s.Metrics) == 0 {
		metrics = []AggregateMetric{{Func: AggCount}}
	} else {
		for _, raw := range s.Metrics {
			m, err := ParseAggregateMetric(raw)
			if err != nil {
				return AggregateInput{}, err
			}
			metrics = append(metrics, m)
		}
	}
	var bucket *TimeBucket
	if s.Bucket != nil {
		b, err := ParseTimeBucket(*s.Bucket)
		if err != nil {
			return AggregateInput{}, err
		}
		bucket = &b
	}

	var filters [][2]string
	for k, v := range s.Filters {
		filters = append(filters, [2]string{k, v})
	}

	limit := DefaultGroups
	if s.Limit != nil {
		limit = *s.Limit
	}

	return AggregateInput{
		Metrics:   metrics,
		GroupBy:   s.GroupBy,
		Bucket:    bucket,
		TimeField: s.TimeField,
		Filters:   filters,
		Since:     s.Since,
		Until:     s.Until,
		Limit:     limit,
	}, nil
}

type PlannedAggregateQuery struct {
	SQL    string
	Params []BindValue
}

// valueExpression resolves one field name to the SQL expression holding its value — a real
// physical column when the field has one, else the entity's own data JSONB path.
func valueExpression(field metadata.EntityField, params *ParamBuilder) string {
	if metadata.FieldHasRealColumn(field) {
		return `"` + field.Name + `"`
	}
	ph := params.Push(TextBind(field.Name))
	return "jsonb_extract_path_text(data, " + ph + ")"
}

// numericExpression: NULLIF(..., ”) first — a missing/blank JSON value comes back as ”, and
// ”::numeric is a hard error at the database rather than the NULL every aggregate function
// already handles correctly.
func numericExpression(field metadata.EntityField, params *ParamBuilder) string {
	return "NULLIF(" + valueExpression(field, params) + ", '')::numeric"
}

// timestampExpression: createdAt/updatedAt are real columns on every table (platform columns,
// not metadata fields), so they never need a metadata lookup.
func timestampExpression(fieldName string, entity metadata.EntityDefinition, params *ParamBuilder) (string, error) {
	switch fieldName {
	case "createdAt":
		return "created_at", nil
	case "updatedAt":
		return "updated_at", nil
	}
	var field *metadata.EntityField
	for i := range entity.Fields {
		if entity.Fields[i].Name == fieldName {
			field = &entity.Fields[i]
			break
		}
	}
	if field == nil {
		return "", invalidAggregate("Unknown field: %s", fieldName)
	}
	if field.Kind != metadata.KindDate && field.Kind != metadata.KindDatetime {
		return "", invalidAggregate("`%s` is not a date field and cannot be used as a time axis.", fieldName)
	}
	return "NULLIF(" + valueExpression(*field, params) + ", '')::timestamptz", nil
}

// assertGroupable: not "any field" — grouping scans and sorts the whole filtered set, so this is
// restricted to fields the entity author already marked as cheap to filter on. status is always
// allowed: the workflow state field every entity that has one groups by first, and it always has
// an index.
func assertGroupable(field metadata.EntityField, entity metadata.EntityDefinition) error {
	inListViewFilters := false
	for _, lv := range entity.ListViews {
		for _, f := range lv.Filters {
			if f == field.Name {
				inListViewFilters = true
			}
		}
	}
	groupable := field.Name == "status" || boolOrFalse(field.Indexed) || field.EnumValues != nil || inListViewFilters
	if !groupable {
		return invalidAggregate("`%s` cannot be grouped by — mark it `indexed`, give it `enum_values`, or add it to a "+
			"list view's filters first.", field.Name)
	}
	return nil
}

// clampInt64 clamps v into [lo, hi].
func clampInt64(v, lo, hi int64) int64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// PlanAggregate has the same signature shape as PlanList (metadata + permissions injected,
// record read policies passed in by the caller that already loaded the snapshot).
func PlanAggregate(metadataRegistry *metadata.MetadataRegistry, permissions *permission.PermissionService, entityName string, input AggregateInput, context *permission.RequestContext, recordReadPolicies []permission.PolicyRow) (*PlannedAggregateQuery, error) {
	entity, ok := metadataRegistry.GetEntity(entityName)
	if !ok {
		return nil, fmt.Errorf("Entity not found: %s", entityName)
	}

	if len(input.Metrics) == 0 {
		return nil, invalidAggregate("At least one metric is required.")
	}
	if len(input.Metrics) > maxMetrics {
		return nil, invalidAggregate("At most %d metrics per request.", maxMetrics)
	}

	tenantID, err := permissions.ScopedTenant(context)
	if err != nil {
		return nil, err
	}
	fieldsByName := map[string]*metadata.EntityField{}
	for i := range entity.Fields {
		fieldsByName[entity.Fields[i].Name] = &entity.Fields[i]
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
	for _, lv := range entity.ListViews {
		for _, f := range lv.Filters {
			allowedFilterFields[f] = true
		}
	}
	for _, kv := range input.Filters {
		fieldName, value := kv[0], kv[1]
		if !allowedFilterFields[fieldName] {
			continue
		}
		field, ok := fieldsByName[fieldName]
		if !ok {
			continue
		}
		expr := valueExpression(*field, params)
		if value == "" {
			conditions = append(conditions, expr+" IS NULL")
		} else {
			ph := params.Push(TextBind(value))
			cast := ""
			if metadata.FieldHasRealColumn(*field) && (field.Kind == metadata.KindReference || field.Kind == metadata.KindID) {
				cast = "::uuid"
			}
			conditions = append(conditions, expr+" = "+ph+cast)
		}
	}

	timeField := "createdAt"
	if input.TimeField != nil {
		timeField = *input.TimeField
	}
	var timeExpr *string
	if input.Bucket != nil || input.Since != nil || input.Until != nil {
		te, err := timestampExpression(timeField, entity, params)
		if err != nil {
			return nil, err
		}
		timeExpr = &te
	}

	if timeExpr != nil {
		if input.Since != nil {
			ph := params.Push(TextBind(*input.Since))
			conditions = append(conditions, *timeExpr+" >= "+ph+"::timestamptz")
		}
		if input.Until != nil {
			ph := params.Push(TextBind(*input.Until))
			conditions = append(conditions, *timeExpr+" < "+ph+"::timestamptz")
		}
	}

	var selectParts, groupParts []string

	if input.Bucket != nil {
		expr := fmt.Sprintf("date_trunc('%s', %s)", input.Bucket.truncUnit(), *timeExpr)
		selectParts = append(selectParts, expr+` AS "bucket"`)
		groupParts = append(groupParts, expr)
	}

	if input.GroupBy != nil {
		field, ok := fieldsByName[*input.GroupBy]
		if !ok {
			return nil, invalidAggregate("Unknown field: %s", *input.GroupBy)
		}
		if err := assertGroupable(*field, entity); err != nil {
			return nil, err
		}
		expr := valueExpression(*field, params)
		// Cast to text unconditionally: a dedicated table's Reference column is a real uuid, and
		// the JSONB path already yields text — one shape out means the caller gets the same JSON
		// type for a group key whichever table the entity happens to live on.
		selectParts = append(selectParts, "("+expr+`)::text AS "group"`)
		groupParts = append(groupParts, "("+expr+")::text")
	}

	for _, metric := range input.Metrics {
		alias := metric.Alias()
		var expr string
		switch {
		case metric.Func == AggCount && metric.Field == nil:
			expr = "count(*)"
		case metric.Field != nil:
			field, ok := fieldsByName[*metric.Field]
			if !ok {
				return nil, invalidAggregate("Unknown field: %s", *metric.Field)
			}
			if metric.Func == AggCount {
				// count:<field> means "rows where this field is set" — the one aggregate that is
				// meaningful over a non-numeric field, so it skips the numeric check.
				expr = "count(" + valueExpression(*field, params) + ")"
			} else {
				if field.Kind != metadata.KindNumber && field.Kind != metadata.KindMoney {
					return nil, invalidAggregate("`%s` is not a numeric field — `%s` needs Number or Money.", field.Name, metric.Func.sqlName())
				}
				expr = metric.Func.sqlName() + "(" + numericExpression(*field, params) + ")"
			}
		default:
			return nil, invalidAggregate("`%s` needs a field.", metric.Func.sqlName())
		}
		// ::float8 on every metric keeps the JSON shape identical across metrics so a client
		// charting library never sees a string for one series and a number for another.
		selectParts = append(selectParts, "("+expr+`)::float8 AS "`+alias+`"`)
	}

	limit := clampInt64(input.Limit, 1, MaxGroups)

	groupBySQL := ""
	if len(groupParts) > 0 {
		groupBySQL = " GROUP BY " + strings.Join(groupParts, ", ")
	}

	// Ordering: chronological when there's a time axis, otherwise biggest-first on the first
	// metric. Neither is caller input.
	orderBySQL := ""
	switch {
	case input.Bucket != nil:
		orderBySQL = ` ORDER BY "bucket" ASC`
	case input.GroupBy != nil:
		orderBySQL = ` ORDER BY "` + input.Metrics[0].Alias() + `" DESC NULLS LAST`
	}

	table := entity.TableName
	whereSQL := strings.Join(conditions, " AND ")
	inner := "SELECT " + strings.Join(selectParts, ", ") + " FROM " + table + " WHERE " + whereSQL +
		groupBySQL + orderBySQL + " LIMIT " + strconv.FormatInt(limit, 10)
	sql := "SELECT to_jsonb(agg) AS row FROM (" + inner + ") agg"

	return &PlannedAggregateQuery{SQL: sql, Params: params.Params}, nil
}
