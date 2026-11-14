//go:build integration

package attachments

import (
	"context"
	"fmt"
	"testing"

	"github.com/metap-org/metap/pkg/testkit"
	"github.com/stretchr/testify/require"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// testPool mirrors the real deployment's convention (control.Router.Begin sets search_path
// to include "metadata" in every tenant transaction).
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := testkit.NewPostgres(t, testkit.WithSearchPath("metadata", "public"))
	return pool
}

func TestCreateListGetDeleteRoundTripAgainstTheSharedTable(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tenant := uuid.New()
	record := uuid.New()
	contentType := "image/png"
	creator := uuid.New()

	created, err := CreateAttachment(ctx, pool, "attachments", tenant, "crm.customers", record,
		"logo.png", "k1", 1234, &contentType, &creator)
	require.NoError(t, err)
	require.Equal(t, "logo.png", created.Filename, "got %+v", created)
	require.EqualValues(t, 1234, created.Size, "got %+v", created)

	list, err := ListAttachments(ctx, pool, "attachments", tenant, "crm.customers", record)
	require.NoError(t, err)
	require.Len(t, list, 1, "got %+v", list)
	require.Equal(t, created.ID, list[0].ID, "got %+v", list)

	got, err := GetAttachment(ctx, pool, "attachments", tenant, created.ID)
	require.NoError(t, err)
	require.NotNil(t, got, "got %+v", got)
	require.Equal(t, "k1", got.Key, "got %+v", got)

	require.NoError(t, DeleteAttachment(ctx, pool, "attachments", tenant, created.ID))
	gone, err := GetAttachment(ctx, pool, "attachments", tenant, created.ID)
	require.NoError(t, err)
	require.Nil(t, gone, "expected the attachment to be gone after delete")
}

func TestDeletingAMissingAttachmentIsNotAnError(t *testing.T) {
	pool := testPool(t)
	require.NoError(t, DeleteAttachment(context.Background(), pool, "attachments", uuid.New(), uuid.New()), "unexpected error")
}

func TestEnsureDedicatedTableIsIdempotentAndIsolatesRows(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	table := "test_go_dedicated_attachments"
	// Clean slate: this test may have left the table behind from a previous run.
	_, _ = pool.Exec(ctx, "DROP TABLE IF EXISTS "+table)

	require.NoError(t, EnsureDedicatedTable(ctx, pool, table))
	require.NoError(t, EnsureDedicatedTable(ctx, pool, table), "second call must be a no-op, got")

	tenant, record := uuid.New(), uuid.New()
	if _, err := CreateAttachment(ctx, pool, table, tenant, "waf.zones", record, "report.pdf", "k2", 42, nil, nil); err != nil {
		require.FailNow(t, fmt.Sprint(err))
	}
	list, err := ListAttachments(ctx, pool, table, tenant, "waf.zones", record)
	require.NoError(t, err)
	require.Len(t, list, 1, "got %d rows in the dedicated table", len(list))

	// The shared default table must be completely unaffected by writes to the dedicated one.
	sharedList, err := ListAttachments(ctx, pool, "attachments", tenant, "waf.zones", record)
	require.NoError(t, err)
	require.Len(t, sharedList, 0, "expected the shared table to be untouched, got %d rows", len(sharedList))

	_, _ = pool.Exec(ctx, "DROP TABLE IF EXISTS "+table)
}
