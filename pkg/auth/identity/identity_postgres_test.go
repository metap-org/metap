//go:build integration

package identity_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/auth/identity"
	"github.com/metap-org/metap/pkg/testkit"
)

func addTenant(t *testing.T, pool *pgxpool.Pool, status, name string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := pool.Exec(context.Background(), `INSERT INTO control.tenants (id, tier, strategy, status, name)
		VALUES ($1, 'trial', 'schema', $2, $3)`, id, status, name)
	require.NoError(t, err)
	return id
}

func TestOnePersonInSeveralTenants(t *testing.T) {
	pool := testkit.NewPostgres(t)
	ctx := context.Background()
	acme := addTenant(t, pool, "active", "Acme")
	beta := addTenant(t, pool, "active", "Beta")
	gone := addTenant(t, pool, "suspended", "Gone")

	first, created, err := identity.AddMember(ctx, pool, identity.NewMember{TenantID: acme, Email: "Ann@Example.com", Password: "first-password"})
	require.NoError(t, err)
	assert.True(t, created)
	// Joining a second tenant reuses the identity and ignores the password offered there.
	second, created, err := identity.AddMember(ctx, pool, identity.NewMember{TenantID: beta, Email: "ann@example.com", Password: "other-password"})
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, first.IdentityID, second.IdentityID)
	assert.NotEqual(t, first.ID, second.ID, "each membership has its own subject id")
	_, _, err = identity.AddMember(ctx, pool, identity.NewMember{TenantID: gone, Email: "ann@example.com"})
	require.NoError(t, err)

	_, _, err = identity.AddMember(ctx, pool, identity.NewMember{TenantID: acme, Email: "ANN@example.com"})
	assert.ErrorIs(t, err, identity.ErrAlreadyMember)

	_, err = identity.Authenticate(ctx, pool, "ann@example.com", "other-password", time.Now())
	assert.ErrorIs(t, err, identity.ErrInvalidCredentials)
	ident, err := identity.Authenticate(ctx, pool, "ANN@example.com", "first-password", time.Now())
	require.NoError(t, err)

	ms, err := identity.Memberships(ctx, pool, ident.ID)
	require.NoError(t, err)
	require.Len(t, ms, 2, "the suspended tenant is not offered")
	assert.Equal(t, "Acme", ms[0].TenantName)
	assert.Equal(t, "Beta", ms[1].TenantName)

	_, ok := identity.PickMembership(ms, nil, nil)
	assert.False(t, ok, "two tenants and none remembered: the person must choose")
	m, ok := identity.PickMembership(ms, nil, &beta)
	require.True(t, ok)
	assert.Equal(t, second.ID, m.MemberID)
	_, ok = identity.PickMembership(ms, &gone, nil)
	assert.False(t, ok)

	require.NoError(t, identity.RecordSignIn(ctx, pool, ident.ID, m.MemberID, beta, time.Now()))
	again, err := identity.Authenticate(ctx, pool, "ann@example.com", "first-password", time.Now())
	require.NoError(t, err)
	assert.Equal(t, beta, *again.LastTenantID)
}

func TestLockoutAfterRepeatedWrongPasswords(t *testing.T) {
	pool := testkit.NewPostgres(t)
	ctx := context.Background()
	tenant := addTenant(t, pool, "active", "Acme")
	_, _, err := identity.AddMember(ctx, pool, identity.NewMember{TenantID: tenant, Email: "bob@example.com", Password: "right-password"})
	require.NoError(t, err)

	now := time.Now()
	for i := 0; i < identity.MaxFailedLogins; i++ {
		_, err := identity.Authenticate(ctx, pool, "bob@example.com", "wrong", now)
		assert.ErrorIs(t, err, identity.ErrInvalidCredentials)
	}
	// Locked: the right password is refused like a wrong one.
	_, err = identity.Authenticate(ctx, pool, "bob@example.com", "right-password", now)
	assert.ErrorIs(t, err, identity.ErrInvalidCredentials)
	_, err = identity.Authenticate(ctx, pool, "bob@example.com", "wrong", now)
	assert.ErrorIs(t, err, identity.ErrInvalidCredentials)

	_, err = identity.Authenticate(ctx, pool, "bob@example.com", "right-password", now.Add(identity.LockoutDuration+time.Second))
	assert.NoError(t, err, "the lock expires")
}

func TestSessionsAreRevokedByDisableRemoveAndPasswordChange(t *testing.T) {
	pool := testkit.NewPostgres(t)
	ctx := context.Background()
	tenant := addTenant(t, pool, "active", "Acme")
	m, _, err := identity.AddMember(ctx, pool, identity.NewMember{TenantID: tenant, Email: "cy@example.com", Password: "pw-123456"})
	require.NoError(t, err)

	issued := time.Now().Add(-time.Minute)
	require.NoError(t, identity.CheckSession(ctx, pool, tenant, m.ID, &issued))
	require.NoError(t, identity.CheckSession(ctx, pool, tenant, uuid.New(), nil), "a non-person subject passes")

	require.NoError(t, identity.SetMemberStatus(ctx, pool, tenant, m.ID, identity.StatusDisabled, time.Now()))
	assert.ErrorIs(t, identity.CheckSession(ctx, pool, tenant, m.ID, &issued), identity.ErrDisabled)
	_, err = identity.Authenticate(ctx, pool, "cy@example.com", "pw-123456", time.Now())
	require.NoError(t, err, "the identity itself is still fine")
	ms, err := identity.Memberships(ctx, pool, m.IdentityID)
	require.NoError(t, err)
	assert.Empty(t, ms, "but the disabled membership is not offered")

	require.NoError(t, identity.SetMemberStatus(ctx, pool, tenant, m.ID, identity.StatusActive, time.Now()))
	assert.ErrorIs(t, identity.CheckSession(ctx, pool, tenant, m.ID, &issued), identity.ErrSessionRevoked,
		"re-enabling does not bring back the sessions disabling revoked")
	fresh := time.Now()
	require.NoError(t, identity.CheckSession(ctx, pool, tenant, m.ID, &fresh))

	changedAt := time.Now()
	require.NoError(t, identity.SetPassword(ctx, pool, m.IdentityID, "new-password", changedAt))
	before := changedAt.Add(-2 * time.Second)
	assert.ErrorIs(t, identity.CheckSession(ctx, pool, tenant, m.ID, &before), identity.ErrSessionRevoked)
	sameSecond := changedAt.Truncate(time.Second)
	assert.NoError(t, identity.CheckSession(ctx, pool, tenant, m.ID, &sameSecond),
		"the session handed back by the change, minted in the same second, survives")
	assert.ErrorIs(t, identity.CheckSession(ctx, pool, tenant, m.ID, nil), identity.ErrSessionRevoked,
		"a token without iat cannot prove it is newer")
	ok, err := identity.CheckPassword(ctx, pool, m.IdentityID, "new-password")
	require.NoError(t, err)
	assert.True(t, ok)

	require.NoError(t, identity.RemoveMember(ctx, pool, tenant, m.ID, time.Now()))
	assert.ErrorIs(t, identity.CheckSession(ctx, pool, tenant, m.ID, &fresh), identity.ErrDisabled)
	_, err = identity.GetMember(ctx, pool, tenant, m.ID)
	assert.ErrorIs(t, err, identity.ErrNotFound)
	// Removed people can be added back (a new membership).
	back, _, err := identity.AddMember(ctx, pool, identity.NewMember{TenantID: tenant, Email: "cy@example.com"})
	require.NoError(t, err)
	assert.NotEqual(t, m.ID, back.ID)
}

func TestListMembersSearchesFiltersPagesAndHidesServicePrincipals(t *testing.T) {
	pool := testkit.NewPostgres(t)
	ctx := context.Background()
	tenant := addTenant(t, pool, "active", "Acme")
	other := addTenant(t, pool, "active", "Other")
	var ids []uuid.UUID
	for _, e := range []string{"dora@example.com", "eve@example.com", "fay@corp.io"} {
		m, _, err := identity.AddMember(ctx, pool, identity.NewMember{TenantID: tenant, Email: e, DisplayName: "Name " + e})
		require.NoError(t, err)
		ids = append(ids, m.ID)
	}
	_, _, err := identity.AddMember(ctx, pool, identity.NewMember{TenantID: other, Email: "zed@example.com"})
	require.NoError(t, err)
	_, _, err = identity.AddMember(ctx, pool, identity.NewMember{TenantID: tenant, Email: "client@service.internal",
		AuthProvider: identity.ServicePrincipalProvider, ExternalSubject: "client-1"})
	require.NoError(t, err)
	require.NoError(t, identity.SetMemberStatus(ctx, pool, tenant, ids[1], identity.StatusDisabled, time.Now()))

	all, total, err := identity.ListMembers(ctx, pool, tenant, identity.ListQuery{})
	require.NoError(t, err)
	assert.Equal(t, 3, total, "other tenants and service principals are not listed")
	assert.Equal(t, "dora@example.com", all[0].Email)
	assert.Equal(t, "Name dora@example.com", all[0].DisplayName)

	found, total, err := identity.ListMembers(ctx, pool, tenant, identity.ListQuery{Search: "EXAMPLE"})
	require.NoError(t, err)
	assert.Equal(t, 2, total)
	assert.Len(t, found, 2)

	disabled, _, err := identity.ListMembers(ctx, pool, tenant, identity.ListQuery{Status: identity.StatusDisabled})
	require.NoError(t, err)
	require.Len(t, disabled, 1)
	assert.Equal(t, "eve@example.com", disabled[0].Email)

	page, total, err := identity.ListMembers(ctx, pool, tenant, identity.ListQuery{Offset: 1, Limit: 1})
	require.NoError(t, err)
	assert.Equal(t, 3, total)
	require.Len(t, page, 1)
	assert.Equal(t, "eve@example.com", page[0].Email)

	some, total, err := identity.ListMembers(ctx, pool, tenant, identity.ListQuery{IDs: []uuid.UUID{ids[2]}})
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	assert.Equal(t, "fay@corp.io", some[0].Email)

	none, total, err := identity.ListMembers(ctx, pool, tenant, identity.ListQuery{IDs: []uuid.UUID{}})
	require.NoError(t, err)
	assert.Zero(t, total, "an empty id filter (a role nobody has) matches nobody")
	assert.Empty(t, none)

	_, _, err = identity.ListMembers(ctx, pool, tenant, identity.ListQuery{Search: `100%_\`})
	require.NoError(t, err, "LIKE metacharacters are escaped")
}
