//go:build integration

package rolestore

import (
	"context"
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
