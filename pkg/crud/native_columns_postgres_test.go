//go:build integration

package crud

// Phase 106 (docs/roadmap/106-native-entity-columns-no-data-jsonb.md): every field is its own typed
// column. These tests pin what that must still look like from the service boundary — a record is a
// JSON object going in and out — and what it must now do that the data jsonb column could not:
// store real types, filter/sort/compare by type, and keep a JSON field's contents exactly.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/metadata"
	"github.com/metap-org/metap/pkg/query"
)

const nativeTable = "entities.test_native_all"

func nativeEntity() metadata.EntityDefinition {
	return metadata.EntityDefinition{
		Name: "test.native_all", Label: "Native", TableName: nativeTable,
		Fields: []metadata.EntityField{
			{Name: "title", Label: "Title", Kind: metadata.KindString, Required: boolPtr(true), Sortable: boolPtr(true)},
			{Name: "qty", Label: "Qty", Kind: metadata.KindNumber, Sortable: boolPtr(true)},
			{Name: "price", Label: "Price", Kind: metadata.KindMoney},
			{Name: "done", Label: "Done", Kind: metadata.KindBoolean},
			{Name: "due", Label: "Due", Kind: metadata.KindDate},
			{Name: "at", Label: "At", Kind: metadata.KindDatetime},
			{Name: "meta", Label: "Meta", Kind: metadata.KindJSON},
			{Name: "note", Label: "Note", Kind: metadata.KindString},
		},
		ListViews: []metadata.EntityListView{{
			Name: "default", Label: "Default", Fields: []string{"title", "qty"}, MaxLimit: 50,
			Filters: []string{"qty", "done", "due", "title"},
		}},
	}
}

func jsonNumber(t *testing.T, raw json.RawMessage) float64 {
	t.Helper()
	var f float64
	require.NoError(t, json.Unmarshal(raw, &f), "not a JSON number: %s", raw)
	return f
}

func TestEveryFieldKindIsStoredInItsOwnTypedColumnAndReadsBackAsJSON(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	pool.Exec(ctx, "DROP TABLE IF EXISTS "+nativeTable+" CASCADE")
	t.Cleanup(func() { pool.Exec(context.Background(), "DROP TABLE IF EXISTS "+nativeTable+" CASCADE") })
	tenantID := uuid.New()
	rc := adminContext(tenantID)
	svc := newCrudService(pool, nativeEntity())

	created, err := svc.Create(ctx, "test.native_all", JsonObject{
		"title": raw("first"), "qty": raw(3), "price": raw(12.5), "done": raw(true),
		"due": raw("2026-01-02"), "at": raw("2026-01-02T03:04:05Z"),
		"meta": json.RawMessage(`{"k":[1,{"z":null}],"s":"x"}`),
	}, rc, nil)
	require.NoError(t, err)
	require.True(t, created.IsOk, "%+v", created)

	got, err := svc.Get(ctx, "test.native_all", created.Data.ID, rc)
	require.NoError(t, err)
	require.True(t, got.IsOk, "%+v", got)
	d := got.Data.Record.Data
	require.JSONEq(t, `"first"`, string(d["title"]))
	require.Equal(t, 3.0, jsonNumber(t, d["qty"]))
	require.Equal(t, 12.5, jsonNumber(t, d["price"]))
	require.JSONEq(t, `true`, string(d["done"]))
	require.JSONEq(t, `"2026-01-02"`, string(d["due"]))
	require.JSONEq(t, `{"k":[1,{"z":null}],"s":"x"}`, string(d["meta"]), "a JSON field must keep its contents, nested nulls included")
	require.NotContains(t, d, "note", "a field with no value is absent, not null")
	var at time.Time
	require.NoError(t, json.Unmarshal(d["at"], &at))
	require.True(t, at.Equal(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)), "got %s", at)

	// The values really are in typed columns, and there is no data column.
	var qty float64
	var done bool
	var due time.Time
	var metaK string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT qty, done, due, meta -> 'k' ->> 0 FROM `+nativeTable+` WHERE id = $1`, created.Data.ID).Scan(&qty, &done, &due, &metaK))
	require.Equal(t, 3.0, qty)
	require.True(t, done)
	require.Equal(t, "2026-01-02", due.Format("2006-01-02"))
	require.Equal(t, "1", metaK)
	var hasData bool
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = 'entities' AND table_name = 'test_native_all' AND column_name = 'data')`).Scan(&hasData))
	require.False(t, hasData)
}

func TestUpdateKeepsFieldsNotInThePayloadAndBumpsVersion(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	pool.Exec(ctx, "DROP TABLE IF EXISTS "+nativeTable+" CASCADE")
	t.Cleanup(func() { pool.Exec(context.Background(), "DROP TABLE IF EXISTS "+nativeTable+" CASCADE") })
	tenantID := uuid.New()
	rc := adminContext(tenantID)
	svc := newCrudService(pool, nativeEntity())

	created, err := svc.Create(ctx, "test.native_all", JsonObject{"title": raw("a"), "qty": raw(7), "note": raw("keep me")}, rc, nil)
	require.NoError(t, err)
	require.True(t, created.IsOk)

	updated, err := svc.Update(ctx, "test.native_all", created.Data.ID, created.Data.Version, JsonObject{"title": raw("b")}, rc, nil)
	require.NoError(t, err)
	require.True(t, updated.IsOk, "%+v", updated)
	require.EqualValues(t, created.Data.Version+1, updated.Data.Version)
	require.JSONEq(t, `"b"`, string(updated.Data.Data["title"]))
	require.Equal(t, 7.0, jsonNumber(t, updated.Data.Data["qty"]), "an untouched field must survive an update")
	require.JSONEq(t, `"keep me"`, string(updated.Data.Data["note"]))

	stale, err := svc.Update(ctx, "test.native_all", created.Data.ID, created.Data.Version, JsonObject{"title": raw("c")}, rc, nil)
	require.NoError(t, err)
	require.False(t, stale.IsOk)
	require.Equal(t, "version_conflict", stale.ErrorCode)
}

func TestFilterSortAndJqlCompareByColumnType(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	pool.Exec(ctx, "DROP TABLE IF EXISTS "+nativeTable+" CASCADE")
	t.Cleanup(func() { pool.Exec(context.Background(), "DROP TABLE IF EXISTS "+nativeTable+" CASCADE") })
	tenantID := uuid.New()
	rc := adminContext(tenantID)
	svc := newCrudService(pool, nativeEntity())

	// qty 10 sorts before 9 as text, not as a number: the point of typed columns. One row has no qty.
	for _, qty := range []any{9, 10, 2, 100, nil} {
		payload := JsonObject{"title": raw("row"), "done": raw(qty != nil && qty.(int) >= 10), "due": raw("2026-03-01")}
		if qty != nil {
			payload["qty"] = raw(qty)
		}
		r, err := svc.Create(ctx, "test.native_all", payload, rc, nil)
		require.NoError(t, err)
		require.True(t, r.IsOk, "%+v", r)
	}

	qtys := func(res ServiceResult[[]RecordDto]) []float64 {
		var out []float64
		for _, rec := range res.Data {
			if v, ok := rec.Data["qty"]; ok {
				out = append(out, jsonNumber(t, v))
			}
		}
		return out
	}

	// Page through every row sorted by qty descending, two at a time: nothing dropped, nothing doubled,
	// numeric (not lexical) order, and the row with no qty comes first under DESC.
	sort := "-qty"
	var all []RecordDto
	var cursor *string
	for pages := 0; pages < 10; pages++ {
		res, err := svc.List(ctx, "test.native_all", query.ListInput{Limit: 2, Sort: &sort, Cursor: cursor}, rc)
		require.NoError(t, err)
		require.True(t, res.IsOk, "%+v", res)
		all = append(all, res.Data...)
		if res.Page == nil || res.Page.NextCursor == nil {
			break
		}
		cursor = res.Page.NextCursor
	}
	require.Len(t, all, 5)
	seen := map[uuid.UUID]bool{}
	for _, r := range all {
		require.False(t, seen[r.ID], "row returned twice")
		seen[r.ID] = true
	}
	require.Equal(t, []float64{100, 10, 9, 2}, qtys(ServiceResult[[]RecordDto]{Data: all}))

	// An exact-match filter casts its text value to the column type.
	byQty, err := svc.List(ctx, "test.native_all", query.ListInput{Limit: 50, Filters: [][2]string{{"qty", "9"}}}, rc)
	require.NoError(t, err)
	require.True(t, byQty.IsOk, "%+v", byQty)
	require.Equal(t, []float64{9}, qtys(byQty))
	byDone, err := svc.List(ctx, "test.native_all", query.ListInput{Limit: 50, Filters: [][2]string{{"done", "true"}}}, rc)
	require.NoError(t, err)
	require.True(t, byDone.IsOk, "%+v", byDone)
	require.Len(t, byDone.Data, 2)
	byDue, err := svc.List(ctx, "test.native_all", query.ListInput{Limit: 50, Filters: [][2]string{{"due", "2026-03-01"}}}, rc)
	require.NoError(t, err)
	require.True(t, byDue.IsOk, "%+v", byDue)
	require.Len(t, byDue.Data, 5)

	// JQL compares numerically: qty > 9 is 10 and 100, not "2" (which is > "10" as text).
	jql := `qty > 9`
	gt, err := svc.List(ctx, "test.native_all", query.ListInput{Limit: 50, JQL: &jql}, rc)
	require.NoError(t, err)
	require.True(t, gt.IsOk, "%+v", gt)
	require.ElementsMatch(t, []float64{10, 100}, qtys(gt))
}

func TestAggregateSumsAMoneyColumnWithoutACastThroughText(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	pool.Exec(ctx, "DROP TABLE IF EXISTS "+nativeTable+" CASCADE")
	t.Cleanup(func() { pool.Exec(context.Background(), "DROP TABLE IF EXISTS "+nativeTable+" CASCADE") })
	tenantID := uuid.New()
	rc := adminContext(tenantID)
	svc := newCrudService(pool, nativeEntity())

	for _, price := range []float64{10.25, 4.75} {
		r, err := svc.Create(ctx, "test.native_all", JsonObject{"title": raw("p"), "price": raw(price)}, rc, nil)
		require.NoError(t, err)
		require.True(t, r.IsOk, "%+v", r)
	}
	field := "price"
	res, err := svc.AggregateWithInput(ctx, "test.native_all",
		query.AggregateInput{Metrics: []query.AggregateMetric{{Func: query.AggSum, Field: &field}}, Limit: query.DefaultGroups}, rc)
	require.NoError(t, err)
	require.True(t, res.IsOk, "%+v", res)
	require.Len(t, res.Data, 1)
	var row map[string]float64
	require.NoError(t, json.Unmarshal(res.Data[0], &row))
	require.Equal(t, 15.0, row["sum_price"])
}
