//go:build integration

package dashboards

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/metap-org/metap/pkg/testkit"
	"github.com/stretchr/testify/require"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// jsonEqual compares two JSON documents by value, not by byte-for-byte formatting — Postgres's
// jsonb round-trips with its own whitespace, which is not a behavior difference worth asserting
// on.
func jsonEqual(t *testing.T, got json.RawMessage, want string) {
	var g, w any
	require.NoError(t, json.Unmarshal(got, &g), "got invalid JSON %s", got)
	require.NoError(t, json.Unmarshal([]byte(want), &w), "want invalid JSON %s", want)
	gb, _ := json.Marshal(g)
	wb, _ := json.Marshal(w)
	require.Equal(t, string(wb), string(gb), "got %s, want %s", got, want)
}

// testPool connects to the same native dev Postgres this session's metap work already uses
// (DATABASE_URL, falling back to the metap/metap convention on localhost:5432) — real e2e,
// matching this repo's own "verify sống" convention rather than mocking the database.
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := testkit.NewPostgres(t)
	return pool
}

func TestUpsertPersonalThenGetPersonalRoundTrips(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tenant, user := uuid.New(), uuid.New()

	cfg, err := UpsertPersonal(ctx, pool, tenant, user, []byte(`{"widgets":["a"]}`))
	require.NoError(t, err)
	require.True(t, cfg.HasOwner, "expected owner %s, got %+v", user, cfg)
	require.Equal(t, user, cfg.OwnerUserID, "expected owner %s, got %+v", user, cfg)

	got, err := GetPersonal(ctx, pool, tenant, user)
	require.NoError(t, err)
	require.NotNil(t, got, "expected a result")
	jsonEqual(t, got.Layout, `{"widgets":["a"]}`)
}

func TestUpsertPersonalTwiceUpdatesInPlace(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tenant, user := uuid.New(), uuid.New()

	_, err := UpsertPersonal(ctx, pool, tenant, user, []byte(`{"v":1}`))
	require.NoError(t, err)
	_, err = UpsertPersonal(ctx, pool, tenant, user, []byte(`{"v":2}`))
	require.NoError(t, err)
	got, _ := GetPersonal(ctx, pool, tenant, user)
	jsonEqual(t, got.Layout, `{"v":2}`)
}

func TestGetEffectiveDashboardPrefersPersonalOverTenantDefault(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tenant, user, admin := uuid.New(), uuid.New(), uuid.New()

	if _, err := UpsertTenantDefault(ctx, pool, tenant, []byte(`{"source":"tenant-default"}`), admin); err != nil {
		require.FailNow(t, fmt.Sprint(err))
	}
	if _, err := UpsertPersonal(ctx, pool, tenant, user, []byte(`{"source":"personal"}`)); err != nil {
		require.FailNow(t, fmt.Sprint(err))
	}

	got, err := GetEffectiveDashboard(ctx, pool, tenant, user)
	require.NoError(t, err)
	jsonEqual(t, got.Layout, `{"source":"personal"}`)
}

func TestGetEffectiveDashboardFallsBackToTenantDefaultWithNoPersonalLayout(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tenant, user, admin := uuid.New(), uuid.New(), uuid.New()

	if _, err := UpsertTenantDefault(ctx, pool, tenant, []byte(`{"source":"tenant-default"}`), admin); err != nil {
		require.FailNow(t, fmt.Sprint(err))
	}

	got, err := GetEffectiveDashboard(ctx, pool, tenant, user)
	require.NoError(t, err)
	require.NotNil(t, got, "expected a result")
	jsonEqual(t, got.Layout, `{"source":"tenant-default"}`)
}

func TestGetEffectiveDashboardIsNilWhenNeitherExists(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tenant, user := uuid.New(), uuid.New()

	got, err := GetEffectiveDashboard(ctx, pool, tenant, user)
	require.NoError(t, err)
	require.Nil(t, got, "expected nil, got %+v", got)
}
