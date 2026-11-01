package tenantauth

// Ported from the Rust metap-auth crate's tests/oidc_e2e.rs and tests/oauth2_login_e2e.rs own
// JIT-provisioning e2e tests (the ones that touch real Postgres).

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

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

// The JIT-provisioning decision: first OIDC login for an externalSubject creates a users row; a
// second login with the same externalSubject must find and reuse that exact row, never create a
// duplicate — the whole reason lookup is by externalSubject, not email.
func TestJitProvisioningCreatesOnceThenLinksOnRepeatLoginOidc(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tenantID := uuid.New()
	t.Cleanup(func() { pool.Exec(context.Background(), "DELETE FROM users WHERE tenant_id = $1", tenantID) })

	noneYet, err := FindOidcUser(ctx, pool, tenantID, "repeat-subject-1")
	if err != nil || noneYet != nil {
		t.Fatalf("expected no user yet, got %+v err=%v", noneYet, err)
	}

	created, err := JitProvisionOidcUser(ctx, pool, tenantID, "first-login@example.com", "repeat-subject-1")
	if err != nil {
		t.Fatal(err)
	}
	if created.Email != "first-login@example.com" {
		t.Fatalf("got email %q", created.Email)
	}

	foundAgain, err := FindOidcUser(ctx, pool, tenantID, "repeat-subject-1")
	if err != nil || foundAgain == nil {
		t.Fatalf("second login must find the row JIT-provisioned by the first, got %+v err=%v", foundAgain, err)
	}
	if foundAgain.ID != created.ID {
		t.Fatal("must link to the same user, not create a duplicate")
	}
}

// Same guarantee, run against the generalized FindExternalUser/JitProvisionExternalUser with
// "oauth2" — confirms the generalization didn't change behavior for either provider.
func TestJitProvisioningCreatesOnceThenLinksOnRepeatLoginOauth2(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tenantID := uuid.New()
	t.Cleanup(func() { pool.Exec(context.Background(), "DELETE FROM users WHERE tenant_id = $1", tenantID) })

	noneYet, err := FindExternalUser(ctx, pool, tenantID, "oauth2", "repeat-subject-1")
	if err != nil || noneYet != nil {
		t.Fatalf("expected no user yet, got %+v err=%v", noneYet, err)
	}

	created, err := JitProvisionExternalUser(ctx, pool, tenantID, "oauth2", "first-login@example.com", "repeat-subject-1")
	if err != nil {
		t.Fatal(err)
	}
	if created.Email != "first-login@example.com" {
		t.Fatalf("got email %q", created.Email)
	}

	foundAgain, err := FindExternalUser(ctx, pool, tenantID, "oauth2", "repeat-subject-1")
	if err != nil || foundAgain == nil {
		t.Fatalf("second login must find the row JIT-provisioned by the first, got %+v err=%v", foundAgain, err)
	}
	if foundAgain.ID != created.ID {
		t.Fatal("must link to the same user, not create a duplicate")
	}
}

// The same externalSubject string under two different providers must resolve to two different
// users, never collide — the reason users_tenant_external_subject_idx is keyed on
// (tenant_id, auth_provider, external_subject), not just (tenant_id, external_subject).
func TestSameExternalSubjectUnderDifferentProvidersDoesNotCollide(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tenantID := uuid.New()
	t.Cleanup(func() { pool.Exec(context.Background(), "DELETE FROM users WHERE tenant_id = $1", tenantID) })

	oidcUser, err := JitProvisionExternalUser(ctx, pool, tenantID, "oidc", "a@example.com", "shared-subject")
	if err != nil {
		t.Fatal(err)
	}
	oauth2User, err := JitProvisionExternalUser(ctx, pool, tenantID, "oauth2", "b@example.com", "shared-subject")
	if err != nil {
		t.Fatal(err)
	}
	if oidcUser.ID == oauth2User.ID {
		t.Fatal("expected two distinct users")
	}

	foundOidc, err := FindExternalUser(ctx, pool, tenantID, "oidc", "shared-subject")
	if err != nil || foundOidc == nil {
		t.Fatal(err)
	}
	foundOauth2, err := FindExternalUser(ctx, pool, tenantID, "oauth2", "shared-subject")
	if err != nil || foundOauth2 == nil {
		t.Fatal(err)
	}
	if foundOidc.ID != oidcUser.ID || foundOauth2.ID != oauth2User.ID {
		t.Fatal("each provider's lookup must resolve to its own user")
	}
}

// EnabledProviders has no dedicated test of its own in the Rust crate's e2e files (confirmed by
// reading them) — new coverage added during the port.
func TestEnabledProvidersReturnsOnlyEnabledRows(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tenantID := uuid.New()
	t.Cleanup(func() {
		pool.Exec(context.Background(), "DELETE FROM tenant_auth_configs WHERE tenant_id = $1", tenantID)
	})

	none, err := EnabledProviders(ctx, pool, tenantID)
	if err != nil || len(none) != 0 {
		t.Fatalf("expected no providers yet, got %v err=%v", none, err)
	}

	if _, err := pool.Exec(ctx,
		"INSERT INTO tenant_auth_configs (tenant_id, provider_kind, enabled, config) VALUES ($1, 'local', true, '{}'::jsonb)",
		tenantID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		"INSERT INTO tenant_auth_configs (tenant_id, provider_kind, enabled, config) VALUES ($1, 'oidc', false, '{}'::jsonb)",
		tenantID); err != nil {
		t.Fatal(err)
	}

	enabled, err := EnabledProviders(ctx, pool, tenantID)
	if err != nil {
		t.Fatal(err)
	}
	if len(enabled) != 1 || enabled[0] != ProviderLocal {
		t.Fatalf("expected only [local], got %v", enabled)
	}
}
