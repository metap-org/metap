//go:build integration

package peripherals

// E2E tests against a real Postgres. Ported from the Rust metap-peripherals crate's
// tests/peripherals_postgres.rs where that file covers role assignment and metadata drift. The
// index_reconciler.rs e2e tests in that file are deliberately NOT ported: they insert into and
// query a table literally named "records", which migrations/0033_drop_records_table.sql
// drops entirely in the current schema — those tests exercise a code path this port's own
// index_reconciler.go doc comment already documents as dead against the current schema, so
// porting them would just assert "fails with relation records does not exist", which is not a
// useful regression test. The pure unit tests (build_index_name/quote_literal/quote_identifier)
// are ported in index_reconciler_test.go instead.
//
// auth.go/context_attributes.go/preferences.go have no dedicated test of their own in the Rust
// crate's test file (confirmed by reading it) — new e2e coverage added during the port.

import (
	"context"
	"fmt"
	"testing"

	"github.com/metap-org/metap/pkg/testkit"
	"github.com/stretchr/testify/require"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/metap-org/metap/pkg/metadata"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := testkit.NewPostgres(t, testkit.WithSearchPath("metadata", "public"))
	return pool
}

func TestRoleAssignmentRoundTrip(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tenantID := uuid.New()
	userID := uuid.New()

	roles, err := GetRolesForUser(ctx, pool, tenantID, userID)
	require.NoError(t, err, "expected no roles yet, got %v err=", roles)
	require.Len(t, roles, 0, "expected no roles yet, got %v err=%v", roles, err)

	require.NoError(t, AssignRole(ctx, pool, tenantID, userID, "sales", nil))
	require.NoError(t, AssignRole(ctx, pool, tenantID, userID, "support", nil))
	// Duplicate assign is a no-op, not an error.
	require.NoError(t, AssignRole(ctx, pool, tenantID, userID, "sales", nil))

	roles, err = GetRolesForUser(ctx, pool, tenantID, userID)
	require.NoError(t, err)
	require.True(t, containsAll(roles, "sales", "support"), "expected [sales support], got %v", roles)
	require.Len(t, roles, 2, "expected [sales support], got %v", roles)

	users, err := ListUsers(ctx, pool, tenantID)
	require.NoError(t, err)
	require.Len(t, users, 1, "expected exactly one user, got %+v", users)
	require.Equal(t, userID, users[0].UserID, "expected exactly one user, got %+v", users)

	require.NoError(t, RevokeRole(ctx, pool, tenantID, userID, "sales"))
	roles, err = GetRolesForUser(ctx, pool, tenantID, userID)
	require.NoError(t, err)
	require.Len(t, roles, 1, "expected [support], got %v", roles)
	require.Equal(t, "support", roles[0], "expected [support], got %v", roles)

	pool.Exec(ctx, "DELETE FROM user_roles WHERE tenant_id = $1", tenantID)
}

func containsAll(haystack []string, needles ...string) bool {
	set := make(map[string]bool, len(haystack))
	for _, h := range haystack {
		set[h] = true
	}
	for _, n := range needles {
		if !set[n] {
			return false
		}
	}
	return true
}

func TestMetadataDriftRecordsFirstBootThenDetectsChange(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	entityName := "test.drift." + uuid.NewString()

	Check(ctx, pool, []metadata.EntitySummary{{Name: entityName, Version: "hash-v1"}})
	var stored string
	require.NoError(t, pool.QueryRow(ctx, "SELECT hash FROM metadata_versions WHERE entity_name = $1", entityName).Scan(&stored))
	require.Equal(t, "hash-v1", stored, "expected hash-v1, got %q", stored)

	// Second call with a different hash — drift is logged, row is updated either way.
	Check(ctx, pool, []metadata.EntitySummary{{Name: entityName, Version: "hash-v2"}})
	require.NoError(t, pool.QueryRow(ctx, "SELECT hash FROM metadata_versions WHERE entity_name = $1", entityName).Scan(&stored))
	require.Equal(t, "hash-v2", stored, "expected hash-v2, got %q", stored)

	pool.Exec(ctx, "DELETE FROM metadata_versions WHERE entity_name = $1", entityName)
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

	ok, err := VerifyCredentials(ctx, pool, email, "correct-password")
	require.NoError(t, err, "expected the correct password to verify, got %+v err=", ok)
	require.NotNil(t, ok, "expected the correct password to verify, got %+v err=%v", ok, err)
	require.Equal(t, created.ID, ok.ID, "expected the correct password to verify, got %+v err=%v", ok, err)

	wrong, err := VerifyCredentials(ctx, pool, email, "wrong-password")
	require.NoError(t, err)
	require.Nil(t, wrong, "expected a wrong password to fail verification")

	missing, err := VerifyCredentials(ctx, pool, "does-not-exist-"+uuid.NewString()+"@example.com", "anything")
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
		" (id uuid PRIMARY KEY, tenant_id uuid NOT NULL, deleted boolean NOT NULL DEFAULT false, data jsonb NOT NULL)"); err != nil {
		require.FailNow(t, fmt.Sprint(err))
	}
	t.Cleanup(func() { pool.Exec(context.Background(), "DROP TABLE IF EXISTS "+tableName) })

	recordID := uuid.New()
	data := `{"userId": "` + userID.String() + `", "department": "security"}`
	if _, err := pool.Exec(ctx, "INSERT INTO "+tableName+" (id, tenant_id, data) VALUES ($1, $2, $3::jsonb)",
		recordID, tenantID, data); err != nil {
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
