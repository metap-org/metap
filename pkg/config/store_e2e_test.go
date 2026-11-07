//go:build integration

package config

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/metap-org/metap/pkg/testkit"
	"github.com/stretchr/testify/require"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := testkit.NewPostgres(t)
	return pool
}

func TestSetPlatformGlobalThenReloadIsVisibleImmediately(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store, err := Load(ctx, pool)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.ResetPlatformGlobal(context.Background(), GraphQLMaxDepth) })

	require.NoError(t, store.SetPlatformGlobal(ctx, GraphQLMaxDepth, num(33)))
	// SetPlatformGlobal already re-swaps the snapshot — no separate Reload needed, same as the
	// Rust ArcSwap store.
	got := store.Current().GetU64(GraphQLMaxDepth)
	require.EqualValues(t, 33, got, "got %d, want 33", got)
}

func TestResetPlatformGlobalFallsBackToTheDeclaredDefault(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store, err := Load(ctx, pool)
	require.NoError(t, err)
	require.NoError(t, store.SetPlatformGlobal(ctx, GraphQLMaxComplexity, num(5000)))
	require.NoError(t, store.ResetPlatformGlobal(ctx, GraphQLMaxComplexity))
	got := store.Current().GetU64(GraphQLMaxComplexity)
	require.EqualValues(t, 1000, got, "got %d, want the declared default 1000", got)
}

func TestSetPlatformGlobalRejectsAnOperatorKey(t *testing.T) {
	pool := testPool(t)
	store, err := Load(context.Background(), pool)
	require.NoError(t, err)
	err = store.SetPlatformGlobal(context.Background(), CORSOrigins, json.RawMessage(`["https://example.com"]`))
	var cfgErr *Error
	require.Error(t, err, "expected an error")
	ok := asConfigError(err, &cfgErr)
	require.True(t, ok, "got %v, want NotWritable", err)
	require.Equal(t, ErrNotWritable, cfgErr.Kind, "got %v, want NotWritable", err)
}

func TestSetPlatformGlobalRejectsAnUnknownKey(t *testing.T) {
	pool := testPool(t)
	store, err := Load(context.Background(), pool)
	require.NoError(t, err)
	err = store.SetPlatformGlobal(context.Background(), "not.a.real.key", num(1))
	var cfgErr *Error
	ok := asConfigError(err, &cfgErr)
	require.True(t, ok, "got %v, want UnknownKey", err)
	require.Equal(t, ErrUnknownKey, cfgErr.Kind, "got %v, want UnknownKey", err)
}

func TestSetPlatformGlobalRejectsAnInvalidValue(t *testing.T) {
	pool := testPool(t)
	store, err := Load(context.Background(), pool)
	require.NoError(t, err)
	err = store.SetPlatformGlobal(context.Background(), GraphQLMaxDepth, num(0))
	var cfgErr *Error
	ok := asConfigError(err, &cfgErr)
	require.True(t, ok, "got %v, want Invalid", err)
	require.Equal(t, ErrInvalid, cfgErr.Kind, "got %v, want Invalid", err)
}

func TestTenantTierRoundTripsThroughLoadTenantAndEffective(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store, err := Load(ctx, pool)
	require.NoError(t, err)
	tenant := uuid.New()

	require.NoError(t, store.SetTenant(ctx, pool, tenant, ThemeDisplayName, str("Acme")))
	tenantSnap, err := store.LoadTenant(ctx, pool, tenant)
	require.NoError(t, err)
	effective := store.Effective(tenantSnap)
	var name string
	_ = json.Unmarshal(effective.Get(ThemeDisplayName), &name)
	require.Equal(t, "Acme", name, "got %q, want Acme", name)

	// CachedTenant must now serve the same snapshot without a DB round trip.
	cached, ok := store.CachedTenant(tenant)
	require.True(t, ok, "expected LoadTenant to have cached the snapshot")
	require.Same(t, tenantSnap, cached, "expected LoadTenant to have cached the snapshot")

	require.NoError(t, store.ResetTenant(ctx, pool, tenant, ThemeDisplayName))
	tenantSnap2, _ := store.LoadTenant(ctx, pool, tenant)
	effective2 := store.Effective(tenantSnap2)
	var name2 string
	_ = json.Unmarshal(effective2.Get(ThemeDisplayName), &name2)
	require.Equal(t, "", name2, "got %q, want the declared default (empty) after reset", name2)
}

func TestSetTenantRejectsAPlatformGlobalKey(t *testing.T) {
	pool := testPool(t)
	store, err := Load(context.Background(), pool)
	require.NoError(t, err)
	err = store.SetTenant(context.Background(), pool, uuid.New(), GraphQLMaxDepth, num(5))
	var cfgErr *Error
	ok := asConfigError(err, &cfgErr)
	require.True(t, ok, "got %v, want NotWritable (a tenant admin reaching a fleet-wide key)", err)
	require.Equal(t, ErrNotWritable, cfgErr.Kind, "got %v, want NotWritable (a tenant admin reaching a fleet-wide key)", err)
}

func TestSetTenantRejectsASecretKeyDirectly(t *testing.T) {
	pool := testPool(t)
	store, err := Load(context.Background(), pool)
	require.NoError(t, err)
	err = store.SetTenant(context.Background(), pool, uuid.New(), CronWebhookAuthorization, str("Bearer abc"))
	var cfgErr *Error
	ok := asConfigError(err, &cfgErr)
	require.True(t, ok, "got %v, want NotWritable (a secret key must go through the secret path)", err)
	require.Equal(t, ErrNotWritable, cfgErr.Kind, "got %v, want NotWritable (a secret key must go through the secret path)", err)
}

func TestSecretMarkerFlowStoresOnlyTheReferenceNeverThePlaintext(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store, err := Load(ctx, pool)
	require.NoError(t, err)
	tenant := uuid.New()

	def, err := store.ValidateTenantSecret(CronWebhookAuthorization, str("Bearer sk_live_abc123"))
	require.NoError(t, err)
	require.True(t, def.Secret, "expected a secret key definition back")

	fakeRef := "vault:webhook-cred:" + tenant.String()
	require.NoError(t, store.SetTenantSecretMarker(ctx, pool, tenant, CronWebhookAuthorization, fakeRef))

	tenantSnap, err := store.LoadTenant(ctx, pool, tenant)
	require.NoError(t, err)
	effective := store.Effective(tenantSnap)
	ref, ok := effective.SecretRef(CronWebhookAuthorization)
	require.True(t, ok, "got (%q, %v), want (%q, true)", ref, ok, fakeRef)
	require.Equal(t, fakeRef, ref, "got (%q, %v), want (%q, true)", ref, ok, fakeRef)

	// The plaintext must never appear anywhere in the stored row.
	var raw json.RawMessage
	require.NoError(t, pool.QueryRow(ctx, "SELECT value FROM metadata.tenant_configs WHERE tenant_id = $1 AND key = $2",
		tenant, CronWebhookAuthorization).Scan(&raw))
	require.NotContains(t, string(raw), "sk_live_abc123", "plaintext credential must never be stored")
}

// asConfigError is errors.As without importing errors in every test file that needs it twice.
func asConfigError(err error, target **Error) bool {
	ce, ok := err.(*Error)
	if !ok {
		return false
	}
	*target = ce
	return true
}
