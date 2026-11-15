//go:build integration

package crud

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/kit/errcode"
	"github.com/metap-org/metap/pkg/metadata"
	"github.com/metap-org/metap/pkg/query"
)

const pagingTable = "entities.test_paging"

func pagingEntity(searchable bool) metadata.EntityDefinition {
	title := metadata.NewField("title", "Title", metadata.KindString).Sort()
	if searchable {
		title = title.Search()
	}
	return metadata.EntityDefinition{
		Name: "test.paging", Label: "Paging", TableName: pagingTable,
		Fields: []metadata.EntityField{title, metadata.NewField("note", "Note", metadata.KindString)},
		ListViews: []metadata.EntityListView{{
			Name: "default", Label: "Default", Fields: []string{"title"}, MaxLimit: 50, DefaultSort: metadata.Ptr("title"),
		}},
	}
}

func TestListOffsetTotalAndSearch(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	pool.Exec(ctx, "DROP TABLE IF EXISTS "+pagingTable+" CASCADE")
	t.Cleanup(func() { pool.Exec(context.Background(), "DROP TABLE IF EXISTS "+pagingTable+" CASCADE") })
	rc := adminContext(uuid.New())
	svc := newCrudService(pool, pagingEntity(true))

	for i := 0; i < 25; i++ {
		title := fmt.Sprintf("alpha-%02d", i)
		if i >= 20 {
			title = fmt.Sprintf("beta-%02d", i)
		}
		_, err := svc.Create(ctx, "test.paging", JsonObject{"title": raw(title)}, rc, nil)
		require.NoError(t, err)
	}
	titles := func(res ListResult) []string {
		var out []string
		for _, r := range res.Data {
			var s string
			require.NoError(t, json.Unmarshal(r.Data["title"], &s))
			out = append(out, s)
		}
		return out
	}

	// Numbered pages: page 3 of 10 is rows 20..24, total is the whole filtered set.
	res, err := svc.List(ctx, "test.paging", query.ListInput{Limit: 10, Offset: 20, WithTotal: true}, rc)
	require.NoError(t, err)
	assert.Equal(t, []string{"beta-20", "beta-21", "beta-22", "beta-23", "beta-24"}, titles(res))
	require.NotNil(t, res.Page.Total)
	assert.EqualValues(t, 25, *res.Page.Total)
	assert.Nil(t, res.Page.NextCursor, "last page has no next cursor")
	assert.True(t, res.Capabilities.CanCreate)

	// Search narrows both the rows and the total, across every searchable field.
	search := "beta"
	res, err = svc.List(ctx, "test.paging", query.ListInput{Limit: 3, Search: &search, WithTotal: true}, rc)
	require.NoError(t, err)
	assert.Len(t, res.Data, 3)
	assert.EqualValues(t, 5, *res.Page.Total)

	// A cursor page does not report a total (it would count only the rows after the cursor).
	first, err := svc.List(ctx, "test.paging", query.ListInput{Limit: 10}, rc)
	require.NoError(t, err)
	require.NotNil(t, first.Page.NextCursor)
	next, err := svc.List(ctx, "test.paging", query.ListInput{Limit: 10, Cursor: first.Page.NextCursor, WithTotal: true}, rc)
	require.NoError(t, err)
	assert.Nil(t, next.Page.Total)

	// Offset and cursor are exclusive; an offset past the cap is refused.
	_, err = svc.List(ctx, "test.paging", query.ListInput{Limit: 10, Offset: 5, Cursor: first.Page.NextCursor}, rc)
	var se *ServiceError
	require.ErrorAs(t, err, &se)
	assert.Equal(t, errcode.InvalidOffset, se.Code)
	_, err = svc.List(ctx, "test.paging", query.ListInput{Limit: 10, Offset: query.MaxListOffset + 1}, rc)
	require.ErrorAs(t, err, &se)
	assert.Equal(t, errcode.InvalidOffset, se.Code)
}

func TestSearchOnEntityWithoutSearchableFieldIsRefused(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	pool.Exec(ctx, "DROP TABLE IF EXISTS "+pagingTable+" CASCADE")
	t.Cleanup(func() { pool.Exec(context.Background(), "DROP TABLE IF EXISTS "+pagingTable+" CASCADE") })
	rc := adminContext(uuid.New())
	svc := newCrudService(pool, pagingEntity(false))

	term := "x"
	_, err := svc.List(ctx, "test.paging", query.ListInput{Limit: 10, Search: &term}, rc)
	var se *ServiceError
	require.ErrorAs(t, err, &se)
	assert.Equal(t, errcode.SearchNotSupported, se.Code)
}
