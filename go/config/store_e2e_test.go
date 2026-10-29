package config

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

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

func TestSetPlatformGlobalThenReloadIsVisibleImmediately(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store, err := Load(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.ResetPlatformGlobal(context.Background(), GraphQLMaxDepth) })

	if err := store.SetPlatformGlobal(ctx, GraphQLMaxDepth, num(33)); err != nil {
		t.Fatal(err)
	}
	// SetPlatformGlobal already re-swaps the snapshot — no separate Reload needed, same as the
	// Rust ArcSwap store.
	if got := store.Current().GetU64(GraphQLMaxDepth); got != 33 {
		t.Fatalf("got %d, want 33", got)
	}
}

func TestResetPlatformGlobalFallsBackToTheDeclaredDefault(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store, err := Load(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetPlatformGlobal(ctx, GraphQLMaxComplexity, num(5000)); err != nil {
		t.Fatal(err)
	}
	if err := store.ResetPlatformGlobal(ctx, GraphQLMaxComplexity); err != nil {
		t.Fatal(err)
	}
	if got := store.Current().GetU64(GraphQLMaxComplexity); got != 1000 {
		t.Fatalf("got %d, want the declared default 1000", got)
	}
}

func TestSetPlatformGlobalRejectsAnOperatorKey(t *testing.T) {
	pool := testPool(t)
	store, err := Load(context.Background(), pool)
	if err != nil {
		t.Fatal(err)
	}
	err = store.SetPlatformGlobal(context.Background(), CORSOrigins, json.RawMessage(`["https://example.com"]`))
	var cfgErr *Error
	if err == nil {
		t.Fatal("expected an error")
	}
	if ok := asConfigError(err, &cfgErr); !ok || cfgErr.Kind != ErrNotWritable {
		t.Fatalf("got %v, want NotWritable", err)
	}
}

func TestSetPlatformGlobalRejectsAnUnknownKey(t *testing.T) {
	pool := testPool(t)
	store, err := Load(context.Background(), pool)
	if err != nil {
		t.Fatal(err)
	}
	err = store.SetPlatformGlobal(context.Background(), "not.a.real.key", num(1))
	var cfgErr *Error
	if ok := asConfigError(err, &cfgErr); !ok || cfgErr.Kind != ErrUnknownKey {
		t.Fatalf("got %v, want UnknownKey", err)
	}
}

func TestSetPlatformGlobalRejectsAnInvalidValue(t *testing.T) {
	pool := testPool(t)
	store, err := Load(context.Background(), pool)
	if err != nil {
		t.Fatal(err)
	}
	err = store.SetPlatformGlobal(context.Background(), GraphQLMaxDepth, num(0))
	var cfgErr *Error
	if ok := asConfigError(err, &cfgErr); !ok || cfgErr.Kind != ErrInvalid {
		t.Fatalf("got %v, want Invalid", err)
	}
}

func TestTenantTierRoundTripsThroughLoadTenantAndEffective(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store, err := Load(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	tenant := uuid.New()

	if err := store.SetTenant(ctx, pool, tenant, ThemeDisplayName, str("Acme")); err != nil {
		t.Fatal(err)
	}
	tenantSnap, err := store.LoadTenant(ctx, pool, tenant)
	if err != nil {
		t.Fatal(err)
	}
	effective := store.Effective(tenantSnap)
	var name string
	_ = json.Unmarshal(effective.Get(ThemeDisplayName), &name)
	if name != "Acme" {
		t.Fatalf("got %q, want Acme", name)
	}

	// CachedTenant must now serve the same snapshot without a DB round trip.
	cached, ok := store.CachedTenant(tenant)
	if !ok || cached != tenantSnap {
		t.Fatal("expected LoadTenant to have cached the snapshot")
	}

	if err := store.ResetTenant(ctx, pool, tenant, ThemeDisplayName); err != nil {
		t.Fatal(err)
	}
	tenantSnap2, _ := store.LoadTenant(ctx, pool, tenant)
	effective2 := store.Effective(tenantSnap2)
	var name2 string
	_ = json.Unmarshal(effective2.Get(ThemeDisplayName), &name2)
	if name2 != "" {
		t.Fatalf("got %q, want the declared default (empty) after reset", name2)
	}
}

func TestSetTenantRejectsAPlatformGlobalKey(t *testing.T) {
	pool := testPool(t)
	store, err := Load(context.Background(), pool)
	if err != nil {
		t.Fatal(err)
	}
	err = store.SetTenant(context.Background(), pool, uuid.New(), GraphQLMaxDepth, num(5))
	var cfgErr *Error
	if ok := asConfigError(err, &cfgErr); !ok || cfgErr.Kind != ErrNotWritable {
		t.Fatalf("got %v, want NotWritable (a tenant admin reaching a fleet-wide key)", err)
	}
}

func TestSetTenantRejectsASecretKeyDirectly(t *testing.T) {
	pool := testPool(t)
	store, err := Load(context.Background(), pool)
	if err != nil {
		t.Fatal(err)
	}
	err = store.SetTenant(context.Background(), pool, uuid.New(), CronWebhookAuthorization, str("Bearer abc"))
	var cfgErr *Error
	if ok := asConfigError(err, &cfgErr); !ok || cfgErr.Kind != ErrNotWritable {
		t.Fatalf("got %v, want NotWritable (a secret key must go through the secret path)", err)
	}
}

func TestSecretMarkerFlowStoresOnlyTheReferenceNeverThePlaintext(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store, err := Load(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	tenant := uuid.New()

	def, err := store.ValidateTenantSecret(CronWebhookAuthorization, str("Bearer sk_live_abc123"))
	if err != nil {
		t.Fatal(err)
	}
	if !def.Secret {
		t.Fatal("expected a secret key definition back")
	}

	fakeRef := "vault:webhook-cred:" + tenant.String()
	if err := store.SetTenantSecretMarker(ctx, pool, tenant, CronWebhookAuthorization, fakeRef); err != nil {
		t.Fatal(err)
	}

	tenantSnap, err := store.LoadTenant(ctx, pool, tenant)
	if err != nil {
		t.Fatal(err)
	}
	effective := store.Effective(tenantSnap)
	ref, ok := effective.SecretRef(CronWebhookAuthorization)
	if !ok || ref != fakeRef {
		t.Fatalf("got (%q, %v), want (%q, true)", ref, ok, fakeRef)
	}

	// The plaintext must never appear anywhere in the stored row.
	var raw json.RawMessage
	if err := pool.QueryRow(ctx, "SELECT value FROM metadata.tenant_configs WHERE tenant_id = $1 AND key = $2",
		tenant, CronWebhookAuthorization).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "sk_live_abc123") {
		t.Fatal("plaintext credential must never be stored")
	}
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
