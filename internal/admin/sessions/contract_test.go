package sessions

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/auth/identity"
)

// fixture is a Store plus what the contract needs from the rest of the system: people who exist and
// sessions that were signed in.
type fixture struct {
	Store
	person func(tenant uuid.UUID) (member uuid.UUID)
	signIn func(tenant, member uuid.UUID, ttl time.Duration, now time.Time) uuid.UUID
}

func ids(rows []identity.Session) map[uuid.UUID]bool {
	out := map[uuid.UUID]bool{}
	for _, r := range rows {
		out[r.ID] = true
	}
	return out
}

// runStoreContract is what every Store must do, run against the in-memory store (unit tests) and
// against PgStore (integration tests), so the two cannot drift apart.
func runStoreContract(t *testing.T, newFixture func(t *testing.T) fixture) {
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)

	t.Run("a membership has an identity, a stranger does not", func(t *testing.T) {
		f := newFixture(t)
		tenant := uuid.New()
		bob := f.person(tenant)
		id, err := f.IdentityOf(ctx, tenant, bob)
		require.NoError(t, err)
		require.NotEqual(t, uuid.Nil, id)
		_, err = f.IdentityOf(ctx, tenant, uuid.New())
		require.ErrorIs(t, err, identity.ErrNotFound)
	})

	t.Run("a list holds the caller's live sessions only", func(t *testing.T) {
		f := newFixture(t)
		tenant := uuid.New()
		bob, eve := f.person(tenant), f.person(tenant)
		laptop := f.signIn(tenant, bob, time.Hour, now)
		phone := f.signIn(tenant, bob, time.Hour, now)
		expired := f.signIn(tenant, bob, time.Hour, now.Add(-2*time.Hour))
		evesOwn := f.signIn(tenant, eve, time.Hour, now)

		bobID, _ := f.IdentityOf(ctx, tenant, bob)
		rows, err := f.List(ctx, bobID, now)
		require.NoError(t, err)
		got := ids(rows)
		require.True(t, got[laptop] && got[phone])
		require.False(t, got[expired], "a session past its expiry is not live")
		require.False(t, got[evesOwn], "someone else's session is not listed")
		require.Len(t, rows, 2)
	})

	t.Run("revoking an own session ends it once, and a stranger's id looks missing", func(t *testing.T) {
		f := newFixture(t)
		tenant := uuid.New()
		bob, eve := f.person(tenant), f.person(tenant)
		phone := f.signIn(tenant, bob, time.Hour, now)
		bobID, _ := f.IdentityOf(ctx, tenant, bob)
		eveID, _ := f.IdentityOf(ctx, tenant, eve)

		require.ErrorIs(t, f.Revoke(ctx, eveID, phone, now), identity.ErrNotFound, "not Eve's to end")
		require.NoError(t, f.Revoke(ctx, bobID, phone, now))
		rows, _ := f.List(ctx, bobID, now)
		require.Empty(t, rows)
		require.ErrorIs(t, f.Revoke(ctx, bobID, phone, now), identity.ErrNotFound, "already ended")
		require.ErrorIs(t, f.Revoke(ctx, bobID, uuid.New(), now), identity.ErrNotFound, "no such session")
	})

	t.Run("revoking the others keeps one and touches nobody else", func(t *testing.T) {
		f := newFixture(t)
		tenant := uuid.New()
		bob, eve := f.person(tenant), f.person(tenant)
		laptop := f.signIn(tenant, bob, time.Hour, now)
		f.signIn(tenant, bob, time.Hour, now)
		f.signIn(tenant, bob, time.Hour, now)
		evesOwn := f.signIn(tenant, eve, time.Hour, now)
		bobID, _ := f.IdentityOf(ctx, tenant, bob)
		eveID, _ := f.IdentityOf(ctx, tenant, eve)

		n, err := f.RevokeOthers(ctx, bobID, laptop, now)
		require.NoError(t, err)
		require.Equal(t, 2, n)
		rows, _ := f.List(ctx, bobID, now)
		require.Len(t, rows, 1)
		require.Equal(t, laptop, rows[0].ID)
		eves, _ := f.List(ctx, eveID, now)
		require.True(t, ids(eves)[evesOwn], "another person's session is untouched")

		n, err = f.RevokeOthers(ctx, bobID, uuid.Nil, now)
		require.NoError(t, err)
		require.Equal(t, 1, n, "a caller that is not a session keeps nothing: every one ends")
	})
}

func TestMemStoreMeetsTheContract(t *testing.T) {
	runStoreContract(t, func(*testing.T) fixture {
		m := newMemStore()
		return fixture{Store: m, person: m.person, signIn: m.signIn}
	})
}
