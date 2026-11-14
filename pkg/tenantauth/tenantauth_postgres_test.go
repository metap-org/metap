//go:build integration

package tenantauth

import (
	"context"
	"testing"

	"github.com/metap-org/metap/pkg/testkit"
	"github.com/stretchr/testify/require"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := testkit.NewPostgres(t, testkit.WithSearchPath("metadata", "public"))
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
	require.NoError(t, err, "expected no user yet, got %+v err=", noneYet)
	require.Nil(t, noneYet, "expected no user yet, got %+v err=%v", noneYet, err)

	created, err := JitProvisionOidcUser(ctx, pool, tenantID, "first-login@example.com", "repeat-subject-1")
	require.NoError(t, err)
	require.Equal(t, "first-login@example.com", created.Email, "got email %q", created.Email)

	foundAgain, err := FindOidcUser(ctx, pool, tenantID, "repeat-subject-1")
	require.NoError(t, err, "second login must find the row JIT-provisioned by the first, got %+v err=", foundAgain)
	require.NotNil(t, foundAgain, "second login must find the row JIT-provisioned by the first, got %+v err=%v", foundAgain, err)
	require.Equal(t, created.ID, foundAgain.ID, "must link to the same user, not create a duplicate")
}

// Same guarantee, run against the generalized FindExternalUser/JitProvisionExternalUser with
// "oauth2" — confirms the generalization didn't change behavior for either provider.
func TestJitProvisioningCreatesOnceThenLinksOnRepeatLoginOauth2(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tenantID := uuid.New()
	t.Cleanup(func() { pool.Exec(context.Background(), "DELETE FROM users WHERE tenant_id = $1", tenantID) })

	noneYet, err := FindExternalUser(ctx, pool, tenantID, "oauth2", "repeat-subject-1")
	require.NoError(t, err, "expected no user yet, got %+v err=", noneYet)
	require.Nil(t, noneYet, "expected no user yet, got %+v err=%v", noneYet, err)

	created, err := JitProvisionExternalUser(ctx, pool, tenantID, "oauth2", "first-login@example.com", "repeat-subject-1")
	require.NoError(t, err)
	require.Equal(t, "first-login@example.com", created.Email, "got email %q", created.Email)

	foundAgain, err := FindExternalUser(ctx, pool, tenantID, "oauth2", "repeat-subject-1")
	require.NoError(t, err, "second login must find the row JIT-provisioned by the first, got %+v err=", foundAgain)
	require.NotNil(t, foundAgain, "second login must find the row JIT-provisioned by the first, got %+v err=%v", foundAgain, err)
	require.Equal(t, created.ID, foundAgain.ID, "must link to the same user, not create a duplicate")
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
	require.NoError(t, err)
	oauth2User, err := JitProvisionExternalUser(ctx, pool, tenantID, "oauth2", "b@example.com", "shared-subject")
	require.NoError(t, err)
	require.NotEqual(t, oauth2User.ID, oidcUser.ID, "expected two distinct users")

	foundOidc, err := FindExternalUser(ctx, pool, tenantID, "oidc", "shared-subject")
	require.NoError(t, err)
	require.NotNil(t, foundOidc, err)
	foundOauth2, err := FindExternalUser(ctx, pool, tenantID, "oauth2", "shared-subject")
	require.NoError(t, err)
	require.NotNil(t, foundOauth2, err)
	require.Equal(t, oidcUser.ID, foundOidc.ID, "each provider's lookup must resolve to its own user")
	require.Equal(t, oauth2User.ID, foundOauth2.ID, "each provider's lookup must resolve to its own user")
}

func TestEnabledProvidersReturnsOnlyEnabledRows(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tenantID := uuid.New()
	t.Cleanup(func() {
		pool.Exec(context.Background(), "DELETE FROM tenant_auth_configs WHERE tenant_id = $1", tenantID)
	})

	none, err := EnabledProviders(ctx, pool, tenantID)
	require.NoError(t, err, "expected no providers yet, got %v err=", none)
	require.Len(t, none, 0, "expected no providers yet, got %v err=%v", none, err)

	_, err = pool.Exec(ctx,
		"INSERT INTO tenant_auth_configs (tenant_id, provider_kind, enabled, config) VALUES ($1, 'local', true, '{}'::jsonb)",
		tenantID)
	require.NoError(t, err)
	_, err = pool.Exec(ctx,
		"INSERT INTO tenant_auth_configs (tenant_id, provider_kind, enabled, config) VALUES ($1, 'oidc', false, '{}'::jsonb)",
		tenantID)
	require.NoError(t, err)

	enabled, err := EnabledProviders(ctx, pool, tenantID)
	require.NoError(t, err)
	require.Len(t, enabled, 1, "expected only [local], got %v", enabled)
	require.Equal(t, ProviderLocal, enabled[0], "expected only [local], got %v", enabled)
}
