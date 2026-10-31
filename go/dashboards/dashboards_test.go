package dashboards

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// jsonEqual compares two JSON documents by value, not by byte-for-byte formatting — Postgres's
// jsonb round-trips with its own whitespace, which is not a behavior difference worth asserting
// on.
func jsonEqual(t *testing.T, got json.RawMessage, want string) {
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("got invalid JSON %s: %v", got, err)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("want invalid JSON %s: %v", want, err)
	}
	gb, _ := json.Marshal(g)
	wb, _ := json.Marshal(w)
	if string(gb) != string(wb) {
		t.Fatalf("got %s, want %s", got, want)
	}
}

// testPool connects to the same native dev Postgres this session's metap work already uses
// (DATABASE_URL, falling back to the metap/metap convention on localhost:5432) — real e2e,
// matching this repo's own "verify sống" convention rather than mocking the database.
func testPool(t *testing.T) *pgxpool.Pool {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		url = "postgres://metap:metap@localhost:5432/metap"
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Skipf("no Postgres available (%v) — skipping e2e test", err)
	}
	if err := pool.Ping(context.Background()); err != nil {
		t.Skipf("no Postgres available (%v) — skipping e2e test", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestUpsertPersonalThenGetPersonalRoundTrips(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tenant, user := uuid.New(), uuid.New()

	cfg, err := UpsertPersonal(ctx, pool, tenant, user, []byte(`{"widgets":["a"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.HasOwner || cfg.OwnerUserID != user {
		t.Fatalf("expected owner %s, got %+v", user, cfg)
	}

	got, err := GetPersonal(ctx, pool, tenant, user)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("expected a result")
	}
	jsonEqual(t, got.Layout, `{"widgets":["a"]}`)
}

func TestUpsertPersonalTwiceUpdatesInPlace(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tenant, user := uuid.New(), uuid.New()

	_, err := UpsertPersonal(ctx, pool, tenant, user, []byte(`{"v":1}`))
	if err != nil {
		t.Fatal(err)
	}
	_, err = UpsertPersonal(ctx, pool, tenant, user, []byte(`{"v":2}`))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := GetPersonal(ctx, pool, tenant, user)
	jsonEqual(t, got.Layout, `{"v":2}`)
}

func TestGetEffectiveDashboardPrefersPersonalOverTenantDefault(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tenant, user, admin := uuid.New(), uuid.New(), uuid.New()

	if _, err := UpsertTenantDefault(ctx, pool, tenant, []byte(`{"source":"tenant-default"}`), admin); err != nil {
		t.Fatal(err)
	}
	if _, err := UpsertPersonal(ctx, pool, tenant, user, []byte(`{"source":"personal"}`)); err != nil {
		t.Fatal(err)
	}

	got, err := GetEffectiveDashboard(ctx, pool, tenant, user)
	if err != nil {
		t.Fatal(err)
	}
	jsonEqual(t, got.Layout, `{"source":"personal"}`)
}

func TestGetEffectiveDashboardFallsBackToTenantDefaultWithNoPersonalLayout(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tenant, user, admin := uuid.New(), uuid.New(), uuid.New()

	if _, err := UpsertTenantDefault(ctx, pool, tenant, []byte(`{"source":"tenant-default"}`), admin); err != nil {
		t.Fatal(err)
	}

	got, err := GetEffectiveDashboard(ctx, pool, tenant, user)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("expected a result")
	}
	jsonEqual(t, got.Layout, `{"source":"tenant-default"}`)
}

func TestGetEffectiveDashboardIsNilWhenNeitherExists(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tenant, user := uuid.New(), uuid.New()

	got, err := GetEffectiveDashboard(ctx, pool, tenant, user)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("expected nil, got %+v", got)
	}
}
