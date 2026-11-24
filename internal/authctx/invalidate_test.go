package authctx

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/permission"
	"github.com/metap-org/metap/pkg/permission/rolestore"
)

// quietDB accepts every statement and finds no rows: enough to run the writes of rolestore that only
// need to exec or to read nothing, so the change notification they send is what is under test.
type quietDB struct{}

func (quietDB) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}
func (quietDB) Query(context.Context, string, ...any) (pgx.Rows, error) { return noRows{}, nil }
func (quietDB) QueryRow(context.Context, string, ...any) pgx.Row        { return nil }

type noRows struct{}

func (noRows) Close()                                       {}
func (noRows) Err() error                                   { return nil }
func (noRows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (noRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (noRows) Next() bool                                   { return false }
func (noRows) Scan(...any) error                            { return nil }
func (noRows) Values() ([]any, error)                       { return nil, nil }
func (noRows) RawValues() [][]byte                          { return nil }
func (noRows) TypeMap() *pgtype.Map                         { return nil }
func (noRows) Conn() *pgx.Conn                              { return nil }

func cached(t *testing.T, c *RolesCache, tenant, user uuid.UUID) bool {
	t.Helper()
	calls := 0
	_, err := c.get(context.Background(), tenant, user, func(context.Context) (callerRoles, error) {
		calls++
		return callerRoles{roles: []string{"viewer"}}, nil
	})
	require.NoError(t, err)
	return calls == 0
}

func TestARoleChangeMadeHereShowsAtOnceForThatMemberOnly(t *testing.T) {
	cache := NewRolesCache(time.Hour)
	defer SubscribeChanges(cache, nil)()
	tenant, alice, bob := uuid.New(), uuid.New(), uuid.New()
	cached(t, cache, tenant, alice)
	cached(t, cache, tenant, bob)
	require.True(t, cached(t, cache, tenant, alice), "premise: both are cached")

	require.NoError(t, rolestore.RevokeRole(context.Background(), quietDB{}, tenant, alice, "admin"))

	require.False(t, cached(t, cache, tenant, alice), "alice's roles are read again")
	require.True(t, cached(t, cache, tenant, bob), "bob's are not touched")
}

func TestEveryRoleWriteTellsTheCache(t *testing.T) {
	ctx := context.Background()
	tenant, user := uuid.New(), uuid.New()
	for name, write := range map[string]func(){
		"revoke":              func() { _ = rolestore.RevokeRole(ctx, quietDB{}, tenant, user, "r") },
		"revoke all":          func() { _ = rolestore.RevokeAllRoles(ctx, quietDB{}, tenant, user) },
		"revoke scoped":       func() { _ = rolestore.RevokeScopedRole(ctx, quietDB{}, tenant, user, "r", "zone", "z1") },
		"revoke scoped of":    func() { _ = rolestore.RevokeScopedRolesOf(ctx, quietDB{}, tenant, user) },
		"revoke scoped scope": func() { _ = rolestore.RevokeScopedRolesOnScope(ctx, quietDB{}, tenant, "zone", "z1") },
	} {
		cache := NewRolesCache(time.Hour)
		unsub := SubscribeChanges(cache, nil)
		cached(t, cache, tenant, user)
		require.True(t, cached(t, cache, tenant, user), name)
		write()
		require.False(t, cached(t, cache, tenant, user), "%s: the cached roles are dropped", name)
		unsub()
	}
}

func TestAScopeGoingAwayDropsEveryMembersRolesOfThatTenantOnly(t *testing.T) {
	cache := NewRolesCache(time.Hour)
	defer SubscribeChanges(cache, nil)()
	tenant, other := uuid.New(), uuid.New()
	a, b, c := uuid.New(), uuid.New(), uuid.New()
	cached(t, cache, tenant, a)
	cached(t, cache, tenant, b)
	cached(t, cache, other, c)

	require.NoError(t, rolestore.RevokeScopedRolesOnScope(context.Background(), quietDB{}, tenant, "zone", "z1"))

	require.False(t, cached(t, cache, tenant, a))
	require.False(t, cached(t, cache, tenant, b))
	require.True(t, cached(t, cache, other, c), "another tenant's members are left alone")
}

func TestSavingRoleGrantsDropsTheTenantsPolicyMatrix(t *testing.T) {
	loads := 0
	tenant := uuid.New()
	load := func(context.Context, uuid.UUID) ([]permission.PolicyRow, error) { loads++; return nil, nil }
	store := permission.NewMatrixStore(nil, load, time.Hour)
	matrix := store.(*permission.MatrixStore)
	read := func() {
		_, err := matrix.FindContextPolicies(context.Background(), tenant, "waf.zones", "read")
		require.NoError(t, err)
	}
	defer SubscribeChanges(NewRolesCache(time.Hour), store)()

	read()
	read()
	require.Equal(t, 1, loads, "premise: the matrix is cached")

	_, err := rolestore.ReplaceRoleBasicGrants(context.Background(), quietDB{}, tenant, "analyst", nil, nil)
	require.NoError(t, err)
	read()
	require.Equal(t, 2, loads, "the saved grants are read at once, not after the time to live")
}

func TestAnUnsubscribedCacheIsNotToldAgain(t *testing.T) {
	cache := NewRolesCache(time.Hour)
	unsub := SubscribeChanges(cache, nil)
	tenant, user := uuid.New(), uuid.New()
	cached(t, cache, tenant, user)
	unsub()
	require.NoError(t, rolestore.RevokeRole(context.Background(), quietDB{}, tenant, user, "r"))
	require.True(t, cached(t, cache, tenant, user))
}
