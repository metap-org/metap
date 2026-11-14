package query

// Pure unit tests for aggregate planning logic.

import (
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/metap-org/metap/pkg/metadata"
	"github.com/metap-org/metap/pkg/permission"
	"github.com/stretchr/testify/require"
)

func TestParseAggregateMetricCount(t *testing.T) {
	m, err := ParseAggregateMetric("count")
	require.NoError(t, err, "got %+v err=", m)
	require.Equal(t, AggCount, m.Func, "got %+v err=%v", m, err)
	require.Nil(t, m.Field, "got %+v err=%v", m, err)
	if m.Alias() != "count" {
		require.FailNow(t, fmt.Sprintf("got alias %q", m.Alias()))
	}
}

func TestParseAggregateMetricSumWithField(t *testing.T) {
	m, err := ParseAggregateMetric("sum:bytesBlocked")
	require.NoError(t, err, "got %+v err=", m)
	require.Equal(t, AggSum, m.Func, "got %+v err=%v", m, err)
	require.NotNil(t, m.Field, "got %+v err=%v", m, err)
	require.Equal(t, "bytesBlocked", *m.Field, "got %+v err=%v", m, err)
	if m.Alias() != "sum_bytesBlocked" {
		require.FailNow(t, fmt.Sprintf("got alias %q", m.Alias()))
	}
}

func TestParseAggregateMetricNonCountNeedsAField(t *testing.T) {
	_, err := ParseAggregateMetric("sum")
	require.Error(t, err, "expected an error")
}

func TestParseAggregateMetricUnknownFunctionIsAnError(t *testing.T) {
	_, err := ParseAggregateMetric("median:x")
	require.Error(t, err, "expected an error")
}

func TestParseTimeBucketKnownValues(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want TimeBucket
	}{
		{"hour", BucketHour}, {"day", BucketDay}, {"week", BucketWeek}, {"month", BucketMonth},
	} {
		got, err := ParseTimeBucket(tc.raw)
		require.NoError(t, err, "%q: got %v err=", tc.raw, got)
		require.Equal(t, tc.want, got, "%q: got %v err=%v", tc.raw, got, err)
	}
}

func TestParseTimeBucketUnknownIsAnError(t *testing.T) {
	_, err := ParseTimeBucket("fortnight")
	require.Error(t, err, "expected an error")
}

func aggTestEntity() metadata.EntityDefinition {
	trueVal := true
	return metadata.EntityDefinition{
		Name:      "test.events",
		Label:     "Event",
		TableName: "entities.test_events",
		Fields: []metadata.EntityField{
			{Name: "status", Label: "Status", Kind: metadata.KindString},
			{Name: "country", Label: "Country", Kind: metadata.KindString, Indexed: &trueVal},
			{Name: "bytesBlocked", Label: "Bytes Blocked", Kind: metadata.KindNumber},
		},
		ListViews: []metadata.EntityListView{
			{Name: "default", Label: "Default", Filters: []string{"status"}, MaxLimit: 50},
		},
	}
}

func TestPlanAggregateDefaultsToACountOfEverything(t *testing.T) {
	reg := qpTestRegistry(t, aggTestEntity())
	perms := permission.NewPermissionService(nil)
	q, err := PlanAggregate(reg, perms, "test.events", AggregateInput{Metrics: []AggregateMetric{{Func: AggCount}}, Limit: DefaultGroups}, qpTestContext(uuid.New()), nil)
	require.NoError(t, err)
	require.Contains(t, q.SQL, "count(*)", "expected count(*), got %q", q.SQL)
	require.Contains(t, q.SQL, "tenant_id = $1", "expected tenant scoping, got %q", q.SQL)
}

func TestPlanAggregateRejectsGroupingByANonGroupableField(t *testing.T) {
	reg := qpTestRegistry(t, aggTestEntity())
	perms := permission.NewPermissionService(nil)
	groupBy := "bytesBlocked" // numeric, not indexed/enum/status/list-view-filter
	_, err := PlanAggregate(reg, perms, "test.events",
		AggregateInput{Metrics: []AggregateMetric{{Func: AggCount}}, GroupBy: &groupBy, Limit: DefaultGroups},
		qpTestContext(uuid.New()), nil)
	_, ok := err.(*InvalidAggregateError)
	require.True(t, ok, "expected an InvalidAggregateError, got %T: %v", err, err)
}

func TestPlanAggregateAllowsGroupingByAnIndexedField(t *testing.T) {
	reg := qpTestRegistry(t, aggTestEntity())
	perms := permission.NewPermissionService(nil)
	groupBy := "country"
	q, err := PlanAggregate(reg, perms, "test.events",
		AggregateInput{Metrics: []AggregateMetric{{Func: AggCount}}, GroupBy: &groupBy, Limit: DefaultGroups},
		qpTestContext(uuid.New()), nil)
	require.NoError(t, err)
	require.Contains(t, q.SQL, "GROUP BY", "expected a GROUP BY clause, got %q", q.SQL)
}

func TestPlanAggregateRejectsSumOverANonNumericField(t *testing.T) {
	reg := qpTestRegistry(t, aggTestEntity())
	perms := permission.NewPermissionService(nil)
	field := "status"
	_, err := PlanAggregate(reg, perms, "test.events",
		AggregateInput{Metrics: []AggregateMetric{{Func: AggSum, Field: &field}}, Limit: DefaultGroups},
		qpTestContext(uuid.New()), nil)
	_, ok := err.(*InvalidAggregateError)
	require.True(t, ok, "expected an InvalidAggregateError, got %T: %v", err, err)
}

func TestPlanAggregateSumOverANumericFieldCastsTheColumnToNumeric(t *testing.T) {
	reg := qpTestRegistry(t, aggTestEntity())
	perms := permission.NewPermissionService(nil)
	field := "bytesBlocked"
	q, err := PlanAggregate(reg, perms, "test.events",
		AggregateInput{Metrics: []AggregateMetric{{Func: AggSum, Field: &field}}, Limit: DefaultGroups},
		qpTestContext(uuid.New()), nil)
	require.NoError(t, err)
	require.Contains(t, q.SQL, `sum(("bytesBlocked")::numeric)`, "expected a numeric-cast sum over the column, got %q", q.SQL)
}

func TestPlanAggregateRejectsMoreThanTheMetricLimit(t *testing.T) {
	reg := qpTestRegistry(t, aggTestEntity())
	perms := permission.NewPermissionService(nil)
	var metrics []AggregateMetric
	for i := 0; i < 11; i++ {
		metrics = append(metrics, AggregateMetric{Func: AggCount})
	}
	_, err := PlanAggregate(reg, perms, "test.events", AggregateInput{Metrics: metrics, Limit: DefaultGroups}, qpTestContext(uuid.New()), nil)
	_, ok := err.(*InvalidAggregateError)
	require.True(t, ok, "expected an InvalidAggregateError, got %T: %v", err, err)
}

func TestPlanAggregateClampsLimitToMaxGroups(t *testing.T) {
	reg := qpTestRegistry(t, aggTestEntity())
	perms := permission.NewPermissionService(nil)
	q, err := PlanAggregate(reg, perms, "test.events",
		AggregateInput{Metrics: []AggregateMetric{{Func: AggCount}}, Limit: 999999},
		qpTestContext(uuid.New()), nil)
	require.NoError(t, err)
	require.Contains(t, q.SQL, "LIMIT 500", "expected the limit clamped to MaxGroups=500, got %q", q.SQL)
}
