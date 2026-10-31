package attachments

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// testPool mirrors the real deployment's convention (metap_control::Router::begin sets
// search_path to include "metadata" in every tenant transaction), so callers here can pass the
// bare table name "attachments" exactly as crates/metap-http/src/routes/attachments.rs does,
// rather than a schema-qualified name validateTableName would reject.
func testPool(t *testing.T) *pgxpool.Pool {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		url = "postgres://metap:metap@localhost:5432/metap"
	}
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, "SET search_path TO metadata, public")
		return err
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Skipf("no Postgres available (%v) — skipping e2e test", err)
	}
	if err := pool.Ping(context.Background()); err != nil {
		t.Skipf("no Postgres available (%v) — skipping e2e test", err)
	}
	t.Cleanup(pool.Close)
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
	if err != nil {
		t.Fatal(err)
	}
	if created.Filename != "logo.png" || created.Size != 1234 {
		t.Fatalf("got %+v", created)
	}

	list, err := ListAttachments(ctx, pool, "attachments", tenant, "crm.customers", record)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != created.ID {
		t.Fatalf("got %+v", list)
	}

	got, err := GetAttachment(ctx, pool, "attachments", tenant, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Key != "k1" {
		t.Fatalf("got %+v", got)
	}

	if err := DeleteAttachment(ctx, pool, "attachments", tenant, created.ID); err != nil {
		t.Fatal(err)
	}
	gone, err := GetAttachment(ctx, pool, "attachments", tenant, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gone != nil {
		t.Fatal("expected the attachment to be gone after delete")
	}
}

func TestDeletingAMissingAttachmentIsNotAnError(t *testing.T) {
	pool := testPool(t)
	if err := DeleteAttachment(context.Background(), pool, "attachments", uuid.New(), uuid.New()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEnsureDedicatedTableIsIdempotentAndIsolatesRows(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	table := "test_go_dedicated_attachments"
	// Clean slate: this test may have left the table behind from a previous run.
	_, _ = pool.Exec(ctx, "DROP TABLE IF EXISTS "+table)

	if err := EnsureDedicatedTable(ctx, pool, table); err != nil {
		t.Fatal(err)
	}
	if err := EnsureDedicatedTable(ctx, pool, table); err != nil {
		t.Fatalf("second call must be a no-op, got: %v", err)
	}

	tenant, record := uuid.New(), uuid.New()
	if _, err := CreateAttachment(ctx, pool, table, tenant, "waf.zones", record, "report.pdf", "k2", 42, nil, nil); err != nil {
		t.Fatal(err)
	}
	list, err := ListAttachments(ctx, pool, table, tenant, "waf.zones", record)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("got %d rows in the dedicated table", len(list))
	}

	// The shared default table must be completely unaffected by writes to the dedicated one.
	sharedList, err := ListAttachments(ctx, pool, "attachments", tenant, "waf.zones", record)
	if err != nil {
		t.Fatal(err)
	}
	if len(sharedList) != 0 {
		t.Fatalf("expected the shared table to be untouched, got %d rows", len(sharedList))
	}

	_, _ = pool.Exec(ctx, "DROP TABLE IF EXISTS "+table)
}
