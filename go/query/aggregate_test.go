package query

// Pure unit tests for aggregate planning logic. See query_planner_test.go's doc comment for why
// this is a reduced-but-real subset of the Rust original's 695-line src/aggregate/tests.rs
// rather than a full 1:1 port — real e2e coverage (proving the generated SQL actually groups/
// counts correctly) lives in query_planner_postgres_test.go.

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/metap-org/metap-go/metadata"
	"github.com/metap-org/metap-go/permission"
)

func TestParseAggregateMetricCount(t *testing.T) {
	m, err := ParseAggregateMetric("count")
	if err != nil || m.Func != AggCount || m.Field != nil {
		t.Fatalf("got %+v err=%v", m, err)
	}
	if m.Alias() != "count" {
		t.Fatalf("got alias %q", m.Alias())
	}
}

func TestParseAggregateMetricSumWithField(t *testing.T) {
	m, err := ParseAggregateMetric("sum:bytesBlocked")
	if err != nil || m.Func != AggSum || m.Field == nil || *m.Field != "bytesBlocked" {
		t.Fatalf("got %+v err=%v", m, err)
	}
	if m.Alias() != "sum_bytesBlocked" {
		t.Fatalf("got alias %q", m.Alias())
	}
}

func TestParseAggregateMetricNonCountNeedsAField(t *testing.T) {
	if _, err := ParseAggregateMetric("sum"); err == nil {
		t.Fatal("expected an error")
	}
}

func TestParseAggregateMetricUnknownFunctionIsAnError(t *testing.T) {
	if _, err := ParseAggregateMetric("median:x"); err == nil {
		t.Fatal("expected an error")
	}
}

func TestParseTimeBucketKnownValues(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want TimeBucket
	}{
		{"hour", BucketHour}, {"day", BucketDay}, {"week", BucketWeek}, {"month", BucketMonth},
	} {
		got, err := ParseTimeBucket(tc.raw)
		if err != nil || got != tc.want {
			t.Fatalf("%q: got %v err=%v", tc.raw, got, err)
		}
	}
}

func TestParseTimeBucketUnknownIsAnError(t *testing.T) {
	if _, err := ParseTimeBucket("fortnight"); err == nil {
		t.Fatal("expected an error")
	}
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
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(q.SQL, "count(*)") {
		t.Fatalf("expected count(*), got %q", q.SQL)
	}
	if !strings.Contains(q.SQL, "tenant_id = $1") {
		t.Fatalf("expected tenant scoping, got %q", q.SQL)
	}
}

func TestPlanAggregateRejectsGroupingByANonGroupableField(t *testing.T) {
	reg := qpTestRegistry(t, aggTestEntity())
	perms := permission.NewPermissionService(nil)
	groupBy := "bytesBlocked" // numeric, not indexed/enum/status/list-view-filter
	_, err := PlanAggregate(reg, perms, "test.events",
		AggregateInput{Metrics: []AggregateMetric{{Func: AggCount}}, GroupBy: &groupBy, Limit: DefaultGroups},
		qpTestContext(uuid.New()), nil)
	if _, ok := err.(*InvalidAggregateError); !ok {
		t.Fatalf("expected an InvalidAggregateError, got %T: %v", err, err)
	}
}

func TestPlanAggregateAllowsGroupingByAnIndexedField(t *testing.T) {
	reg := qpTestRegistry(t, aggTestEntity())
	perms := permission.NewPermissionService(nil)
	groupBy := "country"
	q, err := PlanAggregate(reg, perms, "test.events",
		AggregateInput{Metrics: []AggregateMetric{{Func: AggCount}}, GroupBy: &groupBy, Limit: DefaultGroups},
		qpTestContext(uuid.New()), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(q.SQL, "GROUP BY") {
		t.Fatalf("expected a GROUP BY clause, got %q", q.SQL)
	}
}

func TestPlanAggregateRejectsSumOverANonNumericField(t *testing.T) {
	reg := qpTestRegistry(t, aggTestEntity())
	perms := permission.NewPermissionService(nil)
	field := "status"
	_, err := PlanAggregate(reg, perms, "test.events",
		AggregateInput{Metrics: []AggregateMetric{{Func: AggSum, Field: &field}}, Limit: DefaultGroups},
		qpTestContext(uuid.New()), nil)
	if _, ok := err.(*InvalidAggregateError); !ok {
		t.Fatalf("expected an InvalidAggregateError, got %T: %v", err, err)
	}
}

func TestPlanAggregateSumOverANumericFieldUsesNullifNumericCast(t *testing.T) {
	reg := qpTestRegistry(t, aggTestEntity())
	perms := permission.NewPermissionService(nil)
	field := "bytesBlocked"
	q, err := PlanAggregate(reg, perms, "test.events",
		AggregateInput{Metrics: []AggregateMetric{{Func: AggSum, Field: &field}}, Limit: DefaultGroups},
		qpTestContext(uuid.New()), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(q.SQL, "sum(NULLIF(") || !strings.Contains(q.SQL, "::numeric)") {
		t.Fatalf("expected a NULLIF(...)::numeric sum, got %q", q.SQL)
	}
}

func TestPlanAggregateRejectsMoreThanTheMetricLimit(t *testing.T) {
	reg := qpTestRegistry(t, aggTestEntity())
	perms := permission.NewPermissionService(nil)
	var metrics []AggregateMetric
	for i := 0; i < 11; i++ {
		metrics = append(metrics, AggregateMetric{Func: AggCount})
	}
	_, err := PlanAggregate(reg, perms, "test.events", AggregateInput{Metrics: metrics, Limit: DefaultGroups}, qpTestContext(uuid.New()), nil)
	if _, ok := err.(*InvalidAggregateError); !ok {
		t.Fatalf("expected an InvalidAggregateError, got %T: %v", err, err)
	}
}

func TestPlanAggregateClampsLimitToMaxGroups(t *testing.T) {
	reg := qpTestRegistry(t, aggTestEntity())
	perms := permission.NewPermissionService(nil)
	q, err := PlanAggregate(reg, perms, "test.events",
		AggregateInput{Metrics: []AggregateMetric{{Func: AggCount}}, Limit: 999999},
		qpTestContext(uuid.New()), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(q.SQL, "LIMIT 500") {
		t.Fatalf("expected the limit clamped to MaxGroups=500, got %q", q.SQL)
	}
}
