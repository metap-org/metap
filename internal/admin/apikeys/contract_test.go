package apikeys

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/auth/identity"
)

// fixture is a Store plus a way to make members exist.
type fixture struct {
	Store
	member func(tenant uuid.UUID) uuid.UUID
}

// runStoreContract is what every Store must do, run against the in-memory store (unit tests) and
// against PgStore (integration tests), so the two cannot drift apart.
func runStoreContract(t *testing.T, newFixture func(t *testing.T) fixture) {
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)

	t.Run("a key is issued once, with a prefix, and only for a member of the tenant", func(t *testing.T) {
		f := newFixture(t)
		tenant := uuid.New()
		bot := f.member(tenant)
		exp := now.Add(24 * time.Hour)
		token, key, err := f.Create(ctx, tenant, bot, "pipeline", &exp, nil, now)
		require.NoError(t, err)
		require.True(t, identity.IsAPIKey(token))
		require.Contains(t, token, key.Prefix)
		require.Equal(t, "pipeline", key.Name)
		require.Equal(t, bot, key.MemberID)
		require.NotNil(t, key.ExpiresAt)
		require.Nil(t, key.RevokedAt)

		_, _, err = f.Create(ctx, tenant, uuid.New(), "x", nil, nil, now)
		require.ErrorIs(t, err, identity.ErrNotFound, "no such member")
		other := f.member(uuid.New())
		_, _, err = f.Create(ctx, tenant, other, "x", nil, nil, now)
		require.ErrorIs(t, err, identity.ErrNotFound, "a member of another tenant")
	})

	t.Run("a list is the member's own keys, newest first, revoked ones included", func(t *testing.T) {
		f := newFixture(t)
		tenant := uuid.New()
		a, b := f.member(tenant), f.member(tenant)
		_, first, err := f.Create(ctx, tenant, a, "first", nil, nil, now)
		require.NoError(t, err)
		_, second, err := f.Create(ctx, tenant, a, "second", nil, nil, now.Add(time.Minute))
		require.NoError(t, err)
		_, _, err = f.Create(ctx, tenant, b, "not a's", nil, nil, now)
		require.NoError(t, err)
		require.NoError(t, f.Revoke(ctx, tenant, first.ID, now.Add(2*time.Minute)))

		keys, err := f.List(ctx, tenant, a)
		require.NoError(t, err)
		require.Len(t, keys, 2)
		require.Equal(t, second.ID, keys[0].ID)
		require.Equal(t, first.ID, keys[1].ID)
		require.NotNil(t, keys[1].RevokedAt)

		none, err := f.List(ctx, tenant, uuid.New())
		require.NoError(t, err)
		require.NotNil(t, none)
		require.Empty(t, none)
	})

	t.Run("a key is the tenant's own: another tenant sees nothing of it", func(t *testing.T) {
		f := newFixture(t)
		tenant := uuid.New()
		bot := f.member(tenant)
		_, key, err := f.Create(ctx, tenant, bot, "k", nil, nil, now)
		require.NoError(t, err)

		got, err := f.KeyMember(ctx, tenant, key.ID)
		require.NoError(t, err)
		require.Equal(t, bot, got)
		_, err = f.KeyMember(ctx, uuid.New(), key.ID)
		require.ErrorIs(t, err, identity.ErrNotFound)
		_, err = f.KeyMember(ctx, tenant, uuid.New())
		require.ErrorIs(t, err, identity.ErrNotFound)
		require.ErrorIs(t, f.Revoke(ctx, uuid.New(), key.ID, now), identity.ErrNotFound, "another tenant cannot revoke it")
		keys, _ := f.List(ctx, tenant, bot)
		require.Nil(t, keys[0].RevokedAt, "and it was not revoked")
	})

	t.Run("revoking is idempotent and keeps the first moment", func(t *testing.T) {
		f := newFixture(t)
		tenant := uuid.New()
		bot := f.member(tenant)
		_, key, _ := f.Create(ctx, tenant, bot, "k", nil, nil, now)
		require.NoError(t, f.Revoke(ctx, tenant, key.ID, now.Add(time.Minute)))
		require.NoError(t, f.Revoke(ctx, tenant, key.ID, now.Add(time.Hour)))
		keys, _ := f.List(ctx, tenant, bot)
		require.NotNil(t, keys[0].RevokedAt)
		require.WithinDuration(t, now.Add(time.Minute), *keys[0].RevokedAt, time.Second)
	})

	t.Run("an ordinary member is not a support session", func(t *testing.T) {
		f := newFixture(t)
		tenant := uuid.New()
		yes, err := f.IsSupportSession(ctx, tenant, f.member(tenant))
		require.NoError(t, err)
		require.False(t, yes)
	})
}

func TestMemStoreMeetsTheContract(t *testing.T) {
	runStoreContract(t, func(*testing.T) fixture {
		m := newMemStore()
		return fixture{Store: m, member: m.member}
	})
}
