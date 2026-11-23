package authctx

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/permission"
)

// counting returns a fetch that counts its calls and returns the given roles.
func counting(n *int, roles ...string) func(context.Context) (callerRoles, error) {
	return func(context.Context) (callerRoles, error) {
		*n++
		return callerRoles{roles: roles}, nil
	}
}

func TestNilRolesCacheReadsEveryTime(t *testing.T) {
	var c *RolesCache
	tenant, user := uuid.New(), uuid.New()
	calls := 0
	for range 3 {
		got, err := c.get(context.Background(), tenant, user, counting(&calls, "viewer"))
		require.NoError(t, err)
		require.Equal(t, []string{"viewer"}, got.roles)
	}
	require.Equal(t, 3, calls, "no cache: every request reads the roles")
	c.Invalidate(tenant, user) // a nil cache ignores it
}

func TestZeroTTLTurnsTheCacheOff(t *testing.T) {
	require.Nil(t, NewRolesCache(0))
	require.Nil(t, NewRolesCache(-time.Second))
}

func TestRolesAreReusedWithinTheTTL(t *testing.T) {
	c := NewRolesCache(time.Minute)
	tenant, user := uuid.New(), uuid.New()
	calls := 0
	for range 5 {
		got, err := c.get(context.Background(), tenant, user, counting(&calls, "admin"))
		require.NoError(t, err)
		require.Equal(t, []string{"admin"}, got.roles)
	}
	require.Equal(t, 1, calls)
}

func TestRolesAreKeyedByTenantAndUser(t *testing.T) {
	c := NewRolesCache(time.Minute)
	tenant, other := uuid.New(), uuid.New()
	user := uuid.New()
	calls := 0
	_, _ = c.get(context.Background(), tenant, user, counting(&calls, "a"))
	_, _ = c.get(context.Background(), other, user, counting(&calls, "b"))
	_, _ = c.get(context.Background(), tenant, uuid.New(), counting(&calls, "c"))
	require.Equal(t, 3, calls, "the same user in another tenant, and another user, are separate entries")
}

func TestRolesAreReadAgainAfterTheTTL(t *testing.T) {
	c := NewRolesCache(30 * time.Millisecond)
	tenant, user := uuid.New(), uuid.New()
	calls := 0
	_, _ = c.get(context.Background(), tenant, user, counting(&calls, "viewer"))
	time.Sleep(80 * time.Millisecond)
	got, err := c.get(context.Background(), tenant, user, counting(&calls, "admin"))
	require.NoError(t, err)
	require.Equal(t, []string{"admin"}, got.roles, "a changed role is seen once the entry expires")
	require.Equal(t, 2, calls)
}

func TestInvalidateDropsOneMember(t *testing.T) {
	c := NewRolesCache(time.Minute)
	tenant, user := uuid.New(), uuid.New()
	calls := 0
	_, _ = c.get(context.Background(), tenant, user, counting(&calls, "viewer"))
	c.Invalidate(tenant, user)
	got, _ := c.get(context.Background(), tenant, user, counting(&calls, "admin"))
	require.Equal(t, []string{"admin"}, got.roles)
	require.Equal(t, 2, calls)
}

func TestAFailedReadIsNotCached(t *testing.T) {
	c := NewRolesCache(time.Minute)
	tenant, user := uuid.New(), uuid.New()
	boom := errors.New("db down")
	_, err := c.get(context.Background(), tenant, user, func(context.Context) (callerRoles, error) {
		return callerRoles{}, boom
	})
	require.ErrorIs(t, err, boom)
	calls := 0
	got, err := c.get(context.Background(), tenant, user, counting(&calls, "viewer"))
	require.NoError(t, err)
	require.Equal(t, []string{"viewer"}, got.roles)
	require.Equal(t, 1, calls, "the error left nothing behind")
}

func TestACallerEditingItsRolesDoesNotChangeTheNextRequest(t *testing.T) {
	c := NewRolesCache(time.Minute)
	tenant, user := uuid.New(), uuid.New()
	calls := 0
	first, _ := c.get(context.Background(), tenant, user, counting(&calls, "viewer"))
	first.roles[0] = "admin"
	scoped := []permission.ScopedRole{{Role: "analyst"}}
	_, _ = c.get(context.Background(), tenant, user, func(context.Context) (callerRoles, error) {
		return callerRoles{roles: []string{"viewer"}, scoped: scoped}, nil
	})
	second, _ := c.get(context.Background(), tenant, user, counting(&calls, "unused"))
	require.Equal(t, []string{"viewer"}, second.roles)
}
