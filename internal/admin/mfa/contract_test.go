package mfa

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/metap-org/metap/pkg/auth/identity"
)

// fixture is a Store plus what the contract needs from the rest of the system: people who exist, and
// the code an authenticator app would show for a secret at a moment.
type fixture struct {
	Store
	person func(tenant uuid.UUID) (member uuid.UUID)
	code   func(secret string, at time.Time) string
}

// runStoreContract is what every Store must do, run against the in-memory store (unit tests) and
// against PgStore (integration tests), so the two cannot drift apart.
func runStoreContract(t *testing.T, newFixture func(t *testing.T) fixture) {
	ctx := context.Background()
	start := time.Now().UTC().Truncate(time.Second)

	// enrolled returns an identity with the factor on, its secret, and a moment later than the one
	// used to enrol (a time based code works once per step, so each use moves on).
	enrolled := func(t *testing.T, f fixture) (uuid.UUID, string, time.Time) {
		tenant := uuid.New()
		id, err := f.IdentityOf(ctx, tenant, f.person(tenant))
		require.NoError(t, err)
		secret, uri, err := f.Begin(ctx, id, "me@example.com", start)
		require.NoError(t, err)
		require.NotEmpty(t, secret)
		require.Contains(t, uri, "otpauth://")
		codes, err := f.Confirm(ctx, id, f.code(secret, start), start)
		require.NoError(t, err)
		require.Len(t, codes, 10)
		return id, secret, start.Add(time.Minute)
	}

	t.Run("a membership has an identity and an address, a stranger does not", func(t *testing.T) {
		f := newFixture(t)
		tenant := uuid.New()
		id, err := f.IdentityOf(ctx, tenant, f.person(tenant))
		require.NoError(t, err)
		email, err := f.Email(ctx, id)
		require.NoError(t, err)
		require.NotEmpty(t, email)
		_, err = f.IdentityOf(ctx, tenant, uuid.New())
		require.ErrorIs(t, err, identity.ErrNotFound)
	})

	t.Run("it starts off, and enrolment turns it on only with the right code", func(t *testing.T) {
		f := newFixture(t)
		tenant := uuid.New()
		id, _ := f.IdentityOf(ctx, tenant, f.person(tenant))

		on, left, err := f.Status(ctx, id)
		require.NoError(t, err)
		require.False(t, on)
		require.Zero(t, left)

		_, err = f.Confirm(ctx, id, "000000", start)
		require.ErrorIs(t, err, identity.ErrMFAEnrollNotRun, "confirming before beginning")

		secret, _, err := f.Begin(ctx, id, "me@example.com", start)
		require.NoError(t, err)
		on, _, _ = f.Status(ctx, id)
		require.False(t, on, "beginning does not turn it on")
		_, err = f.Confirm(ctx, id, "not-a-code", start)
		require.ErrorIs(t, err, identity.ErrMFAInvalidCode)
		on, _, _ = f.Status(ctx, id)
		require.False(t, on)

		codes, err := f.Confirm(ctx, id, f.code(secret, start), start)
		require.NoError(t, err)
		require.Len(t, codes, 10)
		on, left, _ = f.Status(ctx, id)
		require.True(t, on)
		require.Equal(t, 10, left)

		_, err = f.Confirm(ctx, id, f.code(secret, start), start)
		require.ErrorIs(t, err, identity.ErrMFAAlreadyOn)
		_, _, err = f.Begin(ctx, id, "me@example.com", start)
		require.ErrorIs(t, err, identity.ErrMFAAlreadyOn, "a confirmed factor is not replaced")
	})

	t.Run("disabling needs a code: a wrong one leaves it on, the right one turns it off", func(t *testing.T) {
		f := newFixture(t)
		id, secret, now := enrolled(t, f)
		require.ErrorIs(t, f.Disable(ctx, id, "wrong", now), identity.ErrMFAInvalidCode)
		on, _, _ := f.Status(ctx, id)
		require.True(t, on)

		require.NoError(t, f.Disable(ctx, id, f.code(secret, now), now))
		on, left, _ := f.Status(ctx, id)
		require.False(t, on)
		require.Zero(t, left)
		_, err := f.Regenerate(ctx, id, f.code(secret, now.Add(time.Minute)), now.Add(time.Minute))
		require.ErrorIs(t, err, identity.ErrMFANotEnrolled, "nothing to regenerate once it is off")
	})

	t.Run("regenerating needs a code and gives a full set", func(t *testing.T) {
		f := newFixture(t)
		id, secret, now := enrolled(t, f)
		_, err := f.Regenerate(ctx, id, "wrong", now)
		require.ErrorIs(t, err, identity.ErrMFAInvalidCode)
		codes, err := f.Regenerate(ctx, id, f.code(secret, now), now)
		require.NoError(t, err)
		require.Len(t, codes, 10)
		_, left, _ := f.Status(ctx, id)
		require.Equal(t, 10, left)
	})

	t.Run("only an enrolled person can regenerate recovery codes", func(t *testing.T) {
		f := newFixture(t)
		tenant := uuid.New()
		id, _ := f.IdentityOf(ctx, tenant, f.person(tenant))
		_, err := f.Regenerate(ctx, id, "000000", start)
		require.ErrorIs(t, err, identity.ErrMFANotEnrolled)
		require.ErrorIs(t, f.Disable(ctx, id, "000000", start), identity.ErrMFANotEnrolled)
	})

	t.Run("five wrong codes lock the factor, and the count is kept", func(t *testing.T) {
		f := newFixture(t)
		id, _, now := enrolled(t, f)
		for i := 0; i < identity.MaxMFAFailures; i++ {
			require.ErrorIs(t, f.Disable(ctx, id, "wrong", now), identity.ErrMFAInvalidCode, "attempt %d", i+1)
		}
		require.ErrorIs(t, f.Disable(ctx, id, "wrong", now), identity.ErrMFALocked)
		on, _, _ := f.Status(ctx, id)
		require.True(t, on, "locked, still on")
	})
}

func TestMemStoreMeetsTheContract(t *testing.T) {
	runStoreContract(t, func(*testing.T) fixture {
		m := newMemStore()
		return fixture{Store: m, person: m.person, code: func(string, time.Time) string { return goodCode }}
	})
}
