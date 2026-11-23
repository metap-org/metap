//go:build integration

package identity

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/testkit"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	return testkit.NewPostgres(t, testkit.WithSearchPath("metadata", "public"))
}

func TestCreateUserAndVerifyCredentialsRoundTrip(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tenantID := uuid.New()
	email := uuid.NewString() + "@example.com"

	created, err := CreateUser(ctx, pool, tenantID, email, "correct-password")
	require.NoError(t, err)
	t.Cleanup(func() { pool.Exec(ctx, "DELETE FROM users WHERE id = $1", created.ID) })

	require.Equal(t, email, created.Email, "unexpected created user: %+v", created)
	require.Equal(t, tenantID, created.TenantID, "unexpected created user: %+v", created)

	ok, err := VerifyCredentials(ctx, pool, email, "correct-password", nil)
	require.NoError(t, err, "expected the correct password to verify, got %+v err=", ok)
	require.NotNil(t, ok, "expected the correct password to verify, got %+v err=%v", ok, err)
	require.Equal(t, created.ID, ok.ID, "expected the correct password to verify, got %+v err=%v", ok, err)

	wrong, err := VerifyCredentials(ctx, pool, email, "wrong-password", nil)
	require.NoError(t, err)
	require.Nil(t, wrong, "expected a wrong password to fail verification")

	missing, err := VerifyCredentials(ctx, pool, "does-not-exist-"+uuid.NewString()+"@example.com", "anything", nil)
	require.NoError(t, err)
	require.Nil(t, missing, "expected an unknown email to fail verification, not error")

	found, err := FindUserByID(ctx, pool, tenantID, created.ID)
	require.NoError(t, err, "expected to find the user by id, got %+v err=", found)
	require.NotNil(t, found, "expected to find the user by id, got %+v err=%v", found, err)
	require.Equal(t, email, found.Email, "expected to find the user by id, got %+v err=%v", found, err)

	// A user id looked up under the wrong tenant must not resolve.
	wrongTenant, err := FindUserByID(ctx, pool, uuid.New(), created.ID)
	require.NoError(t, err)
	require.Nil(t, wrongTenant, "expected a cross-tenant lookup to find nothing")

	listed, err := ListTenantUsers(ctx, pool, tenantID)
	require.NoError(t, err)
	found2 := false
	for _, u := range listed {
		if u.ID == created.ID {
			found2 = true
		}
	}
	require.True(t, found2, "expected the created user to appear in ListTenantUsers")
}

func TestLocalePreferencesRoundTrip(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tenantID := uuid.New()
	userID := uuid.New()

	locale, err := GetLocale(ctx, pool, tenantID, userID)
	require.NoError(t, err)
	require.Equal(t, DefaultLocale, locale, "expected default locale %q, got %q", DefaultLocale, locale)

	require.NoError(t, SetLocale(ctx, pool, tenantID, userID, "vi"))
	locale, err = GetLocale(ctx, pool, tenantID, userID)
	require.NoError(t, err)
	require.Equal(t, "vi", locale, "expected vi, got %q", locale)

	// Setting it again (an upsert, not a duplicate-key error) changes it in place.
	require.NoError(t, SetLocale(ctx, pool, tenantID, userID, "en"))
	locale, err = GetLocale(ctx, pool, tenantID, userID)
	require.NoError(t, err)
	require.Equal(t, "en", locale, "expected en, got %q", locale)

	pool.Exec(ctx, "DELETE FROM user_preferences WHERE tenant_id = $1 AND user_id = $2", tenantID, userID)
}

func TestFetchContextAttributesReadsTheCallersOwnRecordByUserID(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tenantID := uuid.New()
	userID := uuid.New()

	// A dedicated, schema-qualified table — created ad hoc for this test, same convention as
	// every other e2e test in this port that needs its own fixture table rather than depending
	// on a specific business entity being registered.
	tableName := "public.test_context_attrs_" + fastUUIDSuffix()
	if _, err := pool.Exec(ctx, "CREATE TABLE "+tableName+
		" (id uuid PRIMARY KEY, tenant_id uuid NOT NULL, deleted boolean NOT NULL DEFAULT false, \"userId\" uuid, department text)"); err != nil {
		require.FailNow(t, fmt.Sprint(err))
	}
	t.Cleanup(func() { pool.Exec(context.Background(), "DROP TABLE IF EXISTS "+tableName) })

	recordID := uuid.New()
	if _, err := pool.Exec(ctx, "INSERT INTO "+tableName+" (id, tenant_id, \"userId\", department) VALUES ($1, $2, $3, 'security')",
		recordID, tenantID, userID); err != nil {
		require.FailNow(t, fmt.Sprint(err))
	}

	attrs, err := FetchContextAttributes(ctx, pool, tenantID, tableName, userID)
	require.NoError(t, err)
	require.NotNil(t, attrs, "expected department=security, got %v", attrs)
	require.Equal(t, "security", attrs["department"], "expected department=security, got %v", attrs)

	// A different user id must find nothing.
	none, err := FetchContextAttributes(ctx, pool, tenantID, tableName, uuid.New())
	require.NoError(t, err)
	require.Nil(t, none, "expected no match for an unrelated user id, got %v", none)

	// A soft-deleted row must not be returned even if the userId matches.
	_, err = pool.Exec(ctx, "UPDATE "+tableName+" SET deleted = true WHERE id = $1", recordID)
	require.NoError(t, err)
	deleted, err := FetchContextAttributes(ctx, pool, tenantID, tableName, userID)
	require.NoError(t, err)
	require.Nil(t, deleted, "expected a soft-deleted row to be excluded, got %v", deleted)
}

func fastUUIDSuffix() string {
	id := uuid.New().String()
	return "t" + id[:8]
}
